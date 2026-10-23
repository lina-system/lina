// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/internal/design"
)

type alphaRecoveryReply struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// A successful worker receipt is not proof that its conversation response was
// delivered. Bind the reply to the exact input and recovered worker turn, and
// wait for that response's own turn to commit.
func alphaRecoveredReply(ctx context.Context, db *canon.Store, conversation string, attempt recovery.Record) (alphaRecoveryReply, error) {
	var reply alphaRecoveryReply
	err := db.Read(ctx, func(tx *canon.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT coalesce(t.state,''),coalesce(e.id,''),coalesce(e.body,'')
 FROM conv_event w
 LEFT JOIN inbox i ON i.native_id=w.id AND i.source='worker-finish' AND i.conversation=w.conversation_id
 LEFT JOIN turn_runs t ON t.inbox_id=i.id AND t.conversation=w.conversation_id
 LEFT JOIN conv_event e ON e.parent_id=w.id AND e.conversation_id=w.conversation_id AND e.agent_id=w.agent_id AND e.speaker_kind='agent' AND coalesce(e.scaffold_kind,'')=''
 WHERE w.conversation_id=? AND w.parent_id=? AND w.scaffold_kind='worker_completion'
 AND json_extract(w.body,'$.session')=? AND json_extract(w.body,'$.turn')=? AND json_extract(w.body,'$.status')='done'
 ORDER BY w.rowid DESC,e.rowid DESC LIMIT 1`, conversation, attempt.Input, attempt.Thread.ID, attempt.Thread.TurnID).Scan(&state, &reply.ID, &reply.Body)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if state == "failed" || state == "aborted" {
			return fmt.Errorf("recovered result delivery ended %s", state)
		}
		if state != "completed" {
			reply = alphaRecoveryReply{}
		}
		return nil
	})
	return reply, err
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
			got, err := alphaRecoveredReply(h.ctx, db, conversation, attempt)
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
