// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
)

// The intermittent recovery failure was in the result acceptance, not in
// delivery: a body above the ledger's inline limit is stored as a material and
// leaves conv_event.body null, so a raw-column read found no completion and no
// reply for a delivery that had completed. Reproduce that end to end through
// the installed Core, its recovery continuation and its normal result inbox,
// with the retained output of the recovered turn above the inline limit.
func TestAlphaRecoveredReplySpilledCompletionEndToEnd(t *testing.T) {
	dir := t.TempDir()
	report := alphaFullWorkerReport()
	if len(report) <= ledger.InlineBytes {
		t.Fatalf("a full answer to the acceptance's 40-situation corpus is %d bytes; it must exceed the inline limit %d", len(report), ledger.InlineBytes)
	}
	t.Logf("full 40-situation answer: %d bytes, inline limit %d", len(report), ledger.InlineBytes)
	reply := "회수한 결과를 확인했어요."
	var calls atomic.Int32
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid provider input")
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		if calls.Add(1) == 1 {
			emitPersonaCall(w, "start", "worker_start", map[string]any{"workdir": dir, "task_packet": "Preserve this task's result."})
			return
		}
		emitResponse(w, reply, true)
	})))
	defer server.Close()
	h, bootstrap := installedConversationHarness(t, server.URL)
	seedCodingFixture(t, h, bootstrap)
	bin := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(executable, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=3", "LINA_RECOVERY_DIR="+dir, "LINA_RECOVERY_CASE=spilled", "LINA_RECOVERY_OUTPUT="+report, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connect(core).Close()
		return core
	}
	core := start()
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "spilled-result", "회수 결과를 길게 남겨 줘.")
	waitRecovery(t, h, func() bool {
		raw, err := os.ReadFile(filepath.Join(dir, "recovery-result.txt"))
		return err == nil && len(raw) != 0
	})
	c.Close()
	core.killAndWait() // SIGKILL during the bound turn, as the acceptance does.
	core = start()
	defer h.stopCore(core)
	waitRecovery(t, h, func() bool {
		_, err := os.Stat(filepath.Join(dir, "snapshot-requested"))
		return err == nil
	})
	if err := os.WriteFile(filepath.Join(dir, "snapshot-release"), []byte("read now"), 0600); err != nil {
		t.Fatal(err)
	}
	// Wait for the delivery this case asserts, not for any completed worker
	// delivery: the barrier and the assertions read the same committed rows
	// through a normal read-only connection, so they cannot disagree about
	// this turn. The inspection connection used elsewhere only promises a
	// stable snapshot while writers are excluded.
	last := ""
	waitRecovery(t, h, func() bool {
		state := alphaSpilledDeliveryState(t, h, conversation)
		if state != last {
			t.Logf("recovered delivery state=%q", state)
			last = state
		}
		return state == "completed"
	})

	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	attempt := alphaSpilledRecovery(t, h)
	alphaRecoveryReplyDiagnostic(t, h, db, conversation, attempt)
	stored := alphaSpilledCompletion(t, db, h.root, conversation, attempt)
	if stored.body != "" || stored.material == "" {
		t.Fatalf("completion storage: body_bytes=%d material=%q; the fixture did not cross the inline boundary", len(stored.body), stored.material)
	}
	if stored.bytes <= ledger.InlineBytes {
		t.Fatalf("retained completion is %d bytes, not above the inline limit %d", stored.bytes, ledger.InlineBytes)
	}
	// The delivery completed: the completion's inbox entry owns a completed turn.
	var state string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, `SELECT t.state FROM inbox i JOIN turn_runs t ON t.inbox_id=i.id WHERE i.native_id=? AND i.source='worker-finish'`, stored.id).Scan(&state)
	}); err != nil {
		t.Fatalf("the completed delivery has no turn: %v", err)
	}
	if state != "completed" {
		t.Fatalf("delivery turn state=%s, want completed", state)
	}
	t.Logf("retained completion: %d bytes stored as a material; delivery turn completed", stored.bytes)
	// Raw-column attribution finds nothing for a completed delivery. This is
	// the acceptance's historical read and the reported failure.
	if got, err := alphaRecoveredReply(h.ctx, db, alphaInlineBodies{}, conversation, attempt); err != nil || got != (alphaRecoveryReply{}) {
		t.Fatalf("raw-column attribution=%+v err=%v; expected the empty result that failed the acceptance", got, err)
	}
	got, err := alphaRecoveredReply(h.ctx, db, alphaLedgerBodies{h.root}, conversation, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != reply {
		t.Fatalf("attributed reply=%q, want %q", got.Body, reply)
	}
	var replyConversation, replyAgent string
	var parent, scaffold string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, `SELECT conversation_id,agent_id,coalesce(parent_id,''),coalesce(scaffold_kind,'') FROM conv_event WHERE id=?`, got.ID).Scan(&replyConversation, &replyAgent, &parent, &scaffold)
	}); err != nil {
		t.Fatal(err)
	}
	if replyConversation != conversation || parent != stored.id || scaffold != "" || replyAgent == "" {
		t.Fatalf("reply attribution changed: conversation=%s parent=%s scaffold=%s agent=%s", replyConversation, parent, scaffold, replyAgent)
	}
	if directory := os.Getenv("LINA_ALPHA_CAPTURES"); directory != "" {
		raw, err := json.MarshalIndent(map[string]any{"reply": got, "completion_bytes": stored.bytes, "inline_limit": ledger.InlineBytes}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		directory = filepath.Join(directory, t.Name())
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "spilled-recovery.json"), append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// The worker journey in TestAlphaOwnerWorkerCrash asks for 40 situations, three
// sentences each. A worker that answers in full therefore returns more than the
// ledger's inline body limit, so its completion is stored as a material.
func alphaFullWorkerReport() string {
	var report strings.Builder
	for situation := 1; situation <= 40; situation++ {
		fmt.Fprintf(&report, "%d. 흙 표면이 마르고 잎 끝이 처지면 손가락으로 삼센티미터 깊이의 수분을 확인한 뒤 뿌리 주변까지 천천히 물을 줍니다. 배수가 느린 화분은 받침에 고인 물을 바로 비우고 다음 날 아침 잎의 상태를 다시 확인해 과습인지 판단합니다. 장마가 이어지는 동안에는 물을 주지 않고 잎과 줄기의 변색과 무름만 살펴보며 흙이 마르는 속도를 관찰합니다.\n", situation)
	}
	return report.String()
}

type alphaStoredCompletion struct {
	id, body, material string
	revision           int64
	bytes              int
}

// alphaSpilledCompletion reads the retained completion's storage exactly as the
// database holds it, including the column the acceptance used to read.
func alphaSpilledCompletion(t *testing.T, db *canon.Store, root, conversation string, attempt recovery.Record) alphaStoredCompletion {
	t.Helper()
	var stored alphaStoredCompletion
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), `SELECT id,coalesce(body,''),coalesce(material_id,''),coalesce(material_revision,0)
 FROM conv_event WHERE conversation_id=? AND parent_id=? AND scaffold_kind='worker_completion' ORDER BY rowid DESC`, conversation, attempt.Input)
		if err != nil {
			return err
		}
		defer rows.Close()
		// ORDER BY rowid DESC, so the first row is the latest completion.
		if rows.Next() {
			var id, body, material string
			var revision int64
			if err := rows.Scan(&id, &body, &material, &revision); err != nil {
				return err
			}
			stored = alphaStoredCompletion{id: id, body: body, material: material, revision: revision}
			return nil
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if stored.id == "" {
		t.Fatal("retained canon holds no worker completion for the recovered input")
	}
	// Measure the retained bytes from their own storage, independently of the
	// attribution read under test.
	store, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	full, err := store.Read(t.Context(), stored.material, stored.revision)
	if err != nil {
		t.Fatal(err)
	}
	stored.bytes = len(full)
	return stored
}

func alphaSpilledRecovery(t *testing.T, h *harness) recovery.Record {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	records, err := (recovery.Recovery{DB: db}).Records(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].State != "finished" || records[0].Thread.TurnID == "" {
		t.Fatalf("recovery did not settle one finished attempt: %+v", records)
	}
	return records[0]
}

// alphaSpilledDeliveryState names the newest retained completion's delivery
// turn in the conversation: empty until its inbox entry has a turn, then the
// turn's state. Each read uses a normal read-only connection, so the barrier
// sees the same committed rows as the assertions that follow it.
func alphaSpilledDeliveryState(t *testing.T, h *harness, conversation string) string {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	var state string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, `SELECT coalesce((SELECT t.state
 FROM conv_event w
 JOIN inbox i ON i.native_id=w.id AND i.source='worker-finish' AND i.conversation=w.conversation_id
 JOIN turn_runs t ON t.inbox_id=i.id
 WHERE w.conversation_id=? AND w.scaffold_kind='worker_completion'
 ORDER BY w.rowid DESC LIMIT 1),'')`, conversation).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	return state
}
