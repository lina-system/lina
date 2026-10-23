// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func alphaQuestionText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		// Markdown quote prefixes are presentation syntax. An operator inside
		// a sentence is content and must still be visible in the capture.
		for {
			trimmed := strings.TrimLeft(line, " ")
			if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, ">") {
				break
			}
			line = strings.TrimPrefix(strings.TrimPrefix(trimmed, ">"), " ")
		}
		lines[i] = line
	}
	plain := strings.NewReplacer("*", "", "_", "", "`", "", "#", "").Replace(strings.Join(lines, "\n"))
	return strings.Join(strings.Fields(plain), "")
}

func TestAlphaQuestionScreenPreservesQuotedContent(t *testing.T) {
	intro, question, continuation := "The worker is waiting.", "Which name should it use?", "Your answer will reach that worker."
	for _, marker := range []string{"> ", ">> ", "  > > "} {
		source := intro + "\n\n" + marker + "**" + question + "**\n\n" + continuation
		// A wrapped TUI paragraph contains the same content without Markdown
		// delimiters. Removing the question must still fail the observation.
		displayed := "● " + intro + "\n\n" + question + "\n\n" + continuation
		if !strings.Contains(alphaQuestionText(displayed), alphaQuestionText(source)) {
			t.Errorf("visible quoted question was rejected for marker %q", marker)
		}
		missing := "● " + intro + "\n\n" + continuation
		if strings.Contains(alphaQuestionText(missing), alphaQuestionText(source)) {
			t.Error("a missing question was accepted")
		}
	}
	if alphaQuestionText("Is 3 > 2?") == alphaQuestionText("Is 3 2?") {
		t.Error("literal comparison inside a sentence was erased")
	}
}

// A worker's durable question alone does not prove LINA delivered it. An
// unrelated acknowledgement or a provisional response must not release the
// person-answer step. Exercise the same canonical rows the live run reads.
func TestAlphaWorkerQuestionWaitsForDelivery(t *testing.T) {
	h := newHarness(t, buildLina(t))
	core := h.startCore()
	h.connect(core).Close()
	h.stopCore(core)
	db, close := installedAcceptanceStore(t, h)
	defer close()
	write := func(query string, args ...any) {
		t.Helper()
		if err := db.Write(h.ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(h.ctx, query, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	event := func(id, parent, kind, scaffold, body string) {
		t.Helper()
		var parentID any
		if parent != "" {
			parentID = parent
		}
		write(`INSERT INTO conv_event(id,conversation_id,parent_id,agent_id,speaker_kind,surface,scaffold_kind,occurred_at,recorded_at,time_quality,origin,body)
 VALUES(?,'conversation',?,'agent',?,'{}',?,'now','now','recorded','{}',?)`, id, parentID, kind, scaffold, body)
	}
	event("handoff", "", "agent", "", "The worker will ask what name to use.")
	event("completion", "", "tool", "worker_completion", `{"output":"What name should I use?"}`)
	check := func(want string, failed bool) {
		t.Helper()
		got, err := alphaSpokenWorkerQuestion(h.ctx, db)
		if got != want || (err != nil) != failed {
			t.Fatalf("delivered=%q err=%v; want=%q failure=%t", got, err, want, failed)
		}
	}
	check("", false)
	write("INSERT INTO inbox(id,conversation,source,native_id,body) VALUES(1,'conversation','worker-finish','completion','{}')")
	write("INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES('turn:completion','conversation',1,'active')")
	event("response", "completion", "agent", "", "Which name should the worker use?")
	check("", false)
	write("UPDATE turn_runs SET state='failed' WHERE id='turn:completion'")
	check("", true)
	write("UPDATE turn_runs SET state='completed' WHERE id='turn:completion'")
	check("Which name should the worker use?", false)
}

func alphaSpokenWorkerQuestion(ctx context.Context, db *canon.Store) (string, error) {
	var output string
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT w.id,w.body,coalesce(t.state,''),coalesce(e.body,'')
 FROM conv_event w
 LEFT JOIN inbox i ON i.native_id=w.id AND i.source='worker-finish' AND i.conversation=w.conversation_id
 LEFT JOIN turn_runs t ON t.inbox_id=i.id AND t.conversation=w.conversation_id
 LEFT JOIN conv_event e ON e.parent_id=w.id AND e.conversation_id=w.conversation_id AND e.speaker_kind='agent'
 WHERE w.scaffold_kind='worker_completion' ORDER BY w.rowid DESC,e.rowid DESC LIMIT 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			var id, raw, state, reply string
			if err := rows.Scan(&id, &raw, &state, &reply); err != nil {
				return err
			}
			var completion struct{ Output string }
			if err := json.Unmarshal([]byte(raw), &completion); err != nil {
				return err
			}
			if !alphaNameRequest(completion.Output) {
				return nil
			}
			if state == "failed" || state == "aborted" {
				return fmt.Errorf("worker question delivery %s ended %s", id, state)
			}
			if state == "completed" && alphaNameRequest(reply) {
				output = reply
			}
		}
		return rows.Err()
	})
	return output, err
}

func alphaOwnerSpokenQuestion(t *testing.T, h *harness) string {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	reply, err := alphaSpokenWorkerQuestion(h.ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}
