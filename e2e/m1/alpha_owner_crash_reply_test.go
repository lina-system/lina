// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/internal/design"
)

type alphaRecoveryReply struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// A conversation body above the ledger's inline size is stored as a material
// and its event keeps a null body column. Attribution therefore reads the
// committed bytes through the product's own ledger and asset readers; raw SQL
// sees an empty body and silently drops the recovered reply.
type alphaReplyBodies interface {
	read(context.Context, *canon.Store, string) (string, error)
}

// alphaLedgerBodies reads installed events exactly as the product does, so a
// spilled body is verified through the material store rather than skipped.
type alphaLedgerBodies struct{ root string }

func (b alphaLedgerBodies) read(ctx context.Context, db *canon.Store, id string) (string, error) {
	store, err := assets.Open(ctx, b.root, db)
	if err != nil {
		return "", err
	}
	defer store.Close()
	event, err := ledger.New(db, alphaReadMaterials{store}).Get(ctx, id)
	if err != nil {
		return "", err
	}
	return event.Body.Text(), nil
}

// alphaEventBody reads one committed event body, whichever storage the ledger
// chose for it.
func alphaEventBody(ctx context.Context, db *canon.Store, bodies alphaReplyBodies, id string) (string, error) {
	return bodies.read(ctx, db, id)
}

// alphaInlineBodies serves synthetic fixtures whose bodies are all inline.
type alphaInlineBodies struct{}

func (alphaInlineBodies) read(ctx context.Context, db *canon.Store, id string) (string, error) {
	var body string
	err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT coalesce(body,'') FROM conv_event WHERE id=?", id).Scan(&body)
	})
	return body, err
}

// A successful worker receipt is not proof that its conversation response was
// delivered. Bind the reply to the exact input and recovered worker turn, and
// wait for that response's own turn to commit.
func alphaRecoveredReply(ctx context.Context, db *canon.Store, bodies alphaReplyBodies, conversation string, attempt recovery.Record) (alphaRecoveryReply, error) {
	type candidate struct {
		completion string
		reply      string
		state      string
	}
	var candidates []candidate
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT w.id,coalesce(e.id,''),coalesce(t.state,'')
 FROM conv_event w
 LEFT JOIN inbox i ON i.native_id=w.id AND i.source='worker-finish' AND i.conversation=w.conversation_id
 LEFT JOIN turn_runs t ON t.inbox_id=i.id AND t.conversation=w.conversation_id
 LEFT JOIN conv_event e ON e.parent_id=w.id AND e.conversation_id=w.conversation_id AND e.agent_id=w.agent_id AND e.speaker_kind='agent' AND coalesce(e.scaffold_kind,'')=''
 WHERE w.conversation_id=? AND w.parent_id=? AND w.scaffold_kind='worker_completion'
 ORDER BY w.rowid DESC,e.rowid DESC`, conversation, attempt.Input)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.completion, &c.reply, &c.state); err != nil {
				return err
			}
			candidates = append(candidates, c)
		}
		return rows.Err()
	})
	if err != nil {
		return alphaRecoveryReply{}, err
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c.completion] {
			continue
		}
		seen[c.completion] = true
		body, err := bodies.read(ctx, db, c.completion)
		if err != nil {
			return alphaRecoveryReply{}, err
		}
		var result struct {
			Session, Turn, Status string
		}
		if json.Unmarshal([]byte(body), &result) != nil || result.Session != attempt.Thread.ID || result.Turn != attempt.Thread.TurnID || result.Status != "done" {
			continue
		}
		if c.state == "failed" || c.state == "aborted" {
			return alphaRecoveryReply{}, fmt.Errorf("recovered result delivery ended %s", c.state)
		}
		if c.state != "completed" || c.reply == "" {
			return alphaRecoveryReply{}, nil
		}
		reply, err := bodies.read(ctx, db, c.reply)
		if err != nil {
			return alphaRecoveryReply{}, err
		}
		return alphaRecoveryReply{ID: c.reply, Body: reply}, nil
	}
	return alphaRecoveryReply{}, nil
}

func alphaRecoveredReplyVisible(request, reply, screen string) bool {
	if !strings.Contains(screen, design.Text(design.Korean, "live")) || strings.Contains(screen, design.Text(design.Korean, "activity.stop")) {
		return false
	}
	// Long replies scroll their beginning out of view. Require a substantial
	// suffix of the committed answer, not a topic already present in the request.
	runes := []rune(alphaQuestionText(reply))
	if len(runes) == 0 {
		return false
	}
	if len(runes) > 64 {
		runes = runes[len(runes)-64:]
	}
	suffix := string(runes)
	return !strings.Contains(alphaQuestionText(request), suffix) && strings.Contains(alphaQuestionText(screen), suffix)
}

func TestAlphaRecoveredReplyRequiresResultText(t *testing.T) {
	request := "텃밭의 물 관리 검토를 맡겨 줘."
	reply := "검토가 끝났습니다. 흙이 마른 정도와 작물 상태에 따라 물을 주는 기준을 정리했습니다."
	live := design.Text(design.Korean, "live")
	for _, tc := range []struct {
		name, screen string
		want         bool
	}{
		{"request-only", request + "\n" + live, false},
		{"unrelated-reply", request + "\n아직 결과가 없습니다.\n" + live, false},
		{"streaming", request + "\n" + reply + "\n" + design.Text(design.Korean, "activity.stop") + "\n" + live, false},
		{"delivered", request + "\n" + reply + "\n" + live, true},
		{"restored", reply + "\n" + live, true},
		{"disconnected", reply, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := alphaRecoveredReplyVisible(request, reply, tc.screen); got != tc.want {
				t.Fatalf("result visible=%t, want %t", got, tc.want)
			}
		})
	}
	if alphaRecoveredReplyVisible(request, "", request+"\n"+live) || alphaRecoveredReplyVisible(request, request, request+"\n"+live) {
		t.Fatal("missing or echoed result was accepted")
	}
	ending := strings.Repeat("Water only when the roots need it. ", 4)
	long := strings.Repeat("Earlier result paragraph. ", 20) + ending
	if !alphaRecoveredReplyVisible(request, long, ending+"\n"+live) {
		t.Fatal("visible end of a long result was rejected")
	}
}

func TestAlphaRecoveredReplyWaitsForExactDelivery(t *testing.T) {
	h := newHarness(t, buildLina(t))
	core := h.startCore()
	h.connect(core).Close()
	h.stopCore(core)
	db, close := installedAcceptanceStore(t, h)
	defer close()
	for n, name := range []string{
		"active", "failed", "aborted", "completed", "other-input", "other-session", "other-turn", "worker-failed",
		"other-conversation", "other-agent", "unrelated-reply", "scaffold",
	} {
		t.Run(name, func(t *testing.T) {
			write := func(query string, args ...any) {
				t.Helper()
				if err := db.Write(h.ctx, func(tx *canon.Tx) error {
					_, err := tx.ExecContext(h.ctx, query, args...)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			// Each case appends its own immutable ledger events. Altering an
			// existing event would violate the same store contract as production.
			prefix := name + ":"
			conversation := prefix + "conversation"
			event := func(id, parent, conv, agent, kind, scaffold, body string) {
				t.Helper()
				var parentID any
				if parent != "" {
					parentID = prefix + parent
				}
				write(`INSERT INTO conv_event(id,conversation_id,parent_id,agent_id,speaker_kind,surface,scaffold_kind,occurred_at,recorded_at,time_quality,origin,body)
 VALUES(?,?,?,?,?,'{}',?,'now','now','recorded','{}',?)`, prefix+id, conv, parentID, agent, kind, scaffold, body)
			}
			attempt := recovery.Record{Input: prefix + "input", Thread: worker.Thread{ID: "session", TurnID: "turn"}}
			input, session, turn, status, state := "input", "session", "turn", "done", "completed"
			replyConversation, replyAgent, replyParent, scaffold := conversation, "agent", "completion", ""
			switch name {
			case "active", "failed", "aborted":
				state = name
			case "other-input":
				input = "other"
			case "other-session":
				session = "other"
			case "other-turn":
				turn = "other"
			case "worker-failed":
				status = "failed"
			case "other-conversation":
				replyConversation = "other"
			case "other-agent":
				replyAgent = "other"
			case "unrelated-reply":
				replyParent = "input"
			case "scaffold":
				scaffold = "fixture"
			}
			event("input", "", conversation, "agent", "agent", "", "Original task.")
			event("other", "", conversation, "agent", "agent", "", "Unrelated task.")
			body := fmt.Sprintf(`{"session":%q,"turn":%q,"status":%q,"output":"Worker result."}`, session, turn, status)
			event("completion", input, conversation, "agent", "tool", "worker_completion", body)
			inboxID := 1000 + n
			write("INSERT INTO inbox(id,conversation,source,native_id,body) VALUES(?,?,'worker-finish',?,'{}')", inboxID, conversation, prefix+"completion")
			write("INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES(?,?,?,?)", prefix+"delivery", conversation, inboxID, state)
			event("reply", replyParent, replyConversation, replyAgent, "agent", scaffold, "Delivered result.")
			got, err := alphaRecoveredReply(h.ctx, db, alphaInlineBodies{}, conversation, attempt)
			failed := name == "failed" || name == "aborted"
			if (err != nil) != failed {
				t.Fatalf("delivery error=%v, want failure=%t", err, failed)
			}
			if failed {
				return
			}
			want := alphaRecoveryReply{}
			if name == "completed" {
				want = alphaRecoveryReply{ID: prefix + "reply", Body: "Delivered result."}
			}
			if got != want {
				t.Fatalf("reply=%+v, want=%+v", got, want)
			}
		})
	}
}

// A conversation body above the ledger's inline size is stored as a material
// and the event keeps a null body column. The result acceptance must read the
// committed bytes; a completion that carries a long worker report and a long
// reply both cross that boundary, which is what made this check intermittent.
type alphaReadMaterials struct{ store *assets.Store }

func (m alphaReadMaterials) Prepare(context.Context, secrets.Masked) (ledger.MaterialWrite, error) {
	return nil, errors.New("read-only materials")
}

func (m alphaReadMaterials) Read(ctx context.Context, ref ledger.Material) (string, error) {
	file, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	return string(data), err
}

type alphaSeedMaterials struct {
	alphaReadMaterials
	writer *assets.MaterialWriter
}

func (m alphaSeedMaterials) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	prepared, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "conversation-body", assets.Actor{PersonID: "person", AgentID: "agent"})
	if err != nil {
		return nil, err
	}
	return alphaSeedMaterial{prepared}, nil
}

type alphaSeedMaterial struct{ *assets.PreparedMaterial }

func (m alphaSeedMaterial) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	asset, err := m.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: asset.ID, Revision: asset.Revision}, err
}

func TestAlphaRecoveredReplyReadsSpilledBodies(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, _ := acceptanceStore(t, root, 1)
	store, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	writer, err := assets.OpenMaterialWriter(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	materials := alphaSeedMaterials{alphaReadMaterials{store}, writer}
	events := ledger.New(db, materials)
	conversation := "spilled:conversation"
	now := time.Now().UTC()
	input, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "spilled:input", ConversationID: conversation, PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Spilled body task."})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("흙이 마르면 뿌리 깊이를 확인하고 그에 맞춰 물을 줍니다. ", 600)
	if len(long) <= ledger.InlineBytes {
		t.Fatalf("fixture is %d bytes; it must exceed the inline limit %d", len(long), ledger.InlineBytes)
	}
	spilled := func(id string) bool {
		t.Helper()
		var material string
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT coalesce(material_id,'') FROM conv_event WHERE id=?", id).Scan(&material)
		}); err != nil {
			t.Fatal(err)
		}
		return material != ""
	}
	for _, tc := range []struct {
		name, report, reply string
	}{
		{"completion", long, "짧은 결과입니다."},
		{"reply", "", long},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completionBody, err := json.Marshal(struct {
				Session, Turn, Status, Output string
			}{tc.name, "turn", "done", tc.report})
			if err != nil {
				t.Fatal(err)
			}
			completion, err := events.Admit(t.Context(), guard.EntryTool, ledger.Input{ID: "spilled:" + tc.name + ":completion", ConversationID: conversation, ParentID: input.ID, AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, Locator: "worker-session:" + tc.name, ScaffoldKind: "worker_completion", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: string(completionBody)})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: conversation, Source: "worker-finish", NativeID: completion.ID}, Body: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES(?,?,?,'completed')", "turn:"+completion.ID, conversation, receipt.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			reply, err := events.Admit(t.Context(), guard.EntryAgent, ledger.Input{ID: "spilled:" + tc.name + ":reply", ConversationID: conversation, ParentID: completion.ID, AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: tc.reply})
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "completion" && !spilled(completion.ID) {
				t.Fatal("fixture completion body was not stored as a material")
			}
			if tc.name == "reply" && !spilled(reply.ID) {
				t.Fatal("fixture reply body was not stored as a material")
			}
			attempt := recovery.Record{Input: input.ID, Thread: worker.Thread{ID: tc.name, TurnID: "turn"}}
			got, err := alphaRecoveredReply(t.Context(), db, alphaLedgerBodies{root}, conversation, attempt)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != reply.ID || got.Body != tc.reply {
				t.Fatalf("reply id=%q bytes=%d, want id=%q bytes=%d", got.ID, len(got.Body), reply.ID, len(tc.reply))
			}
		})
	}
}
