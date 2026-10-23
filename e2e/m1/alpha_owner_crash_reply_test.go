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
	write := func(query string, args ...any) {
		t.Helper()
		if err := db.Write(h.ctx, func(tx *canon.Tx) error { _, err := tx.ExecContext(h.ctx, query, args...); return err }); err != nil {
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
	attempt := recovery.Record{Input: "input", Thread: worker.Thread{ID: "session", TurnID: "turn"}}
	event("input", "", "agent", "", "Original task.")
	event("other", "", "agent", "", "Unrelated task.")
	event("completion", "input", "tool", "worker_completion", `{"session":"session","turn":"turn","status":"done","output":"Worker result."}`)
	write("INSERT INTO inbox(id,conversation,source,native_id,body) VALUES(1,'conversation','worker-finish','completion','{}')")
	write("INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES('delivery','conversation',1,'active')")
	event("reply", "completion", "agent", "", "Delivered result.")
	check := func(want alphaRecoveryReply, failed bool) {
		t.Helper()
		got, err := alphaRecoveredReply(h.ctx, db, "conversation", attempt)
		if got != want || (err != nil) != failed {
			t.Fatalf("reply=%+v err=%v, want=%+v failure=%t", got, err, want, failed)
		}
	}
	check(alphaRecoveryReply{}, false)
	write("UPDATE turn_runs SET state='failed'")
	if _, err := alphaRecoveredReply(h.ctx, db, "conversation", attempt); err == nil {
		t.Fatal("failed delivery was accepted")
	}
	write("UPDATE turn_runs SET state='completed'")
	want := alphaRecoveryReply{ID: "reply", Body: "Delivered result."}
	check(want, false)
	for _, tc := range []struct{ name, change, restore string }{
		{"other-input", "UPDATE conv_event SET parent_id='other' WHERE id='completion'", "UPDATE conv_event SET parent_id='input' WHERE id='completion'"},
		{"other-session", `UPDATE conv_event SET body=json_set(body,'$.session','other') WHERE id='completion'`, `UPDATE conv_event SET body=json_set(body,'$.session','session') WHERE id='completion'`},
		{"other-turn", `UPDATE conv_event SET body=json_set(body,'$.turn','other') WHERE id='completion'`, `UPDATE conv_event SET body=json_set(body,'$.turn','turn') WHERE id='completion'`},
		{"worker-failed", `UPDATE conv_event SET body=json_set(body,'$.status','failed') WHERE id='completion'`, `UPDATE conv_event SET body=json_set(body,'$.status','done') WHERE id='completion'`},
		{"other-conversation", "UPDATE conv_event SET conversation_id='other' WHERE id='reply'", "UPDATE conv_event SET conversation_id='conversation' WHERE id='reply'"},
		{"other-agent", "UPDATE conv_event SET agent_id='other' WHERE id='reply'", "UPDATE conv_event SET agent_id='agent' WHERE id='reply'"},
		{"unrelated-reply", "UPDATE conv_event SET parent_id='input' WHERE id='reply'", "UPDATE conv_event SET parent_id='completion' WHERE id='reply'"},
		{"scaffold", "UPDATE conv_event SET scaffold_kind='fixture' WHERE id='reply'", "UPDATE conv_event SET scaffold_kind='' WHERE id='reply'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.change)
			check(alphaRecoveryReply{}, false)
			write(tc.restore)
			check(want, false)
		})
	}
}
