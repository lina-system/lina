// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/loop"
)

func doneEvidenceFixture(t *testing.T) *conversationRuntime {
	t.Helper()
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "own-conversation"
	for _, tc := range []struct {
		id, conversation, agent string
		code                    loop.Code
	}{
		{"own", r.conversation, agent, loop.OK},
		{"failed", r.conversation, agent, loop.Failed},
		{"foreign-conversation", "other-conversation", agent, loop.OK},
		{"foreign-agent", r.conversation, "other-agent", loop.OK},
	} {
		for _, id := range []string{"input:" + tc.id, "prepare:" + tc.id} {
			now := time.Now()
			_, err := ledger.New(r.db, nil).Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: id, ConversationID: tc.conversation, AgentID: tc.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "{}"})
			if err != nil {
				t.Fatal(err)
			}
		}
		err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			in, err := tx.ExecContext(t.Context(), "INSERT INTO inbox(conversation,source,native_id,body) VALUES(?,'test',?,X'01')", tc.conversation, tc.id)
			if err != nil {
				return err
			}
			id, err := in.LastInsertId()
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES(?,?,?,'completed')", tc.id, tc.conversation, id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO turn_control(turn_id,input_event) VALUES(?,?)", tc.id, "input:"+tc.id); err != nil {
				return err
			}
			raw, _ := json.Marshal(loop.Result{ID: "read", Code: tc.code, Output: json.RawMessage(`{"source":"observed"}`)})
			_, err = tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,'read',?,?)", tc.id, []byte(`{"name":"read_record"}`), raw)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestDoneEvidenceCannotUseForeignOrFailedObservations(t *testing.T) {
	r := doneEvidenceFixture(t)
	refs := []result.EvidenceReference{{TurnID: "own", CallID: "read"}, {TurnID: "failed", CallID: "read"}, {TurnID: "foreign-conversation", CallID: "read"}, {TurnID: "foreign-agent", CallID: "read"}}
	observed, err := r.doneObservations(t.Context(), refs)
	if err != nil || len(observed) != 1 || !observed[[2]string{"own", "read"}] {
		t.Fatalf("crossed evidence scope: %+v %v", observed, err)
	}
}

// An unrelated result raises a SQL error if selected. This checks bounded reads
// deterministically, without depending on timings or the query's spelling.
func TestDoneEvidenceReadsOnlyCompletionReferences(t *testing.T) {
	r := doneEvidenceFixture(t)
	err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `ALTER TABLE turn_calls RENAME TO evidence_call_rows;
CREATE VIEW turn_calls AS SELECT turn_id,round,ordinal,call_id,call,
 CASE WHEN turn_id='failed' THEN json('not JSON') ELSE result END AS result FROM evidence_call_rows;`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]loop.Tool{}
	for _, name := range []string{"read_record", "write_record", "present_result", "land_work_result", "coding_decision"} {
		bindings[name] = loop.Tool{Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
			var args struct{ Completion *result.CompletionRequest }
			_ = json.Unmarshal([]byte(call.Arguments), &args)
			if call.Name == "read_record" || args.Completion == nil {
				return loop.Result{ID: call.ID, Code: loop.OK}
			}
			var c result.Completion
			// The resolver must finish its reads before the writer's transaction.
			err := r.db.Write(ctx, func(_ *canon.Tx) error { c = result.CompletionFor(ctx, *args.Completion); return nil })
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(c)
			return loop.Result{ID: call.ID, Code: loop.OK, Output: raw}
		}}
	}
	prepared := agents.Preparation{TurnID: "own", WorkMethodRevision: "prepared-policy"}
	if err = r.bindDoneEvidence(t.Context(), prepared, bindings); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, args, want string }{
		{"read_record", `{"completion":{"work_kind":"code","evidence":[{"kind":"owner_path_run","turn_id":"failed","call_id":"read"}]}}`, ""},
		{"write_record", `{}`, ""},
		{"present_result", `{}`, ""},
		{"present_result", `{"completion":{"work_kind":"code","evidence":[{"kind":"owner_path_run","turn_id":"","call_id":"read"}]}}`, "done"},
		{"write_record", `{"completion":{"work_kind":"code","evidence":[{"kind":"owner_path_run","turn_id":"own","call_id":"read"}]}}`, "done"},
		{"land_work_result", `{"completion":{"work_kind":"code","evidence":[{"kind":"owner_path_run","turn_id":"own","call_id":"read"}]}}`, "done"},
		{"coding_decision", `{"completion":{"work_kind":"code","evidence":[{"kind":"owner_path_run","turn_id":"own","call_id":"read"}]}}`, "done"},
		{"coding_decision", `{"completion":{"work_kind":"code","evidence":[{"kind":"owner_path_run","turn_id":"absent","call_id":"read"}]}}`, "claimed"},
		{"coding_decision", `{"completion":{"work_kind":"code"}}`, "claimed"},
	} {
		t.Run(tc.name+"/"+tc.want, func(t *testing.T) {
			got := bindings[tc.name].Execute(t.Context(), provider.ToolCall{ID: "complete", Name: tc.name, Arguments: tc.args})
			if got.Code != loop.OK {
				t.Fatalf("read unrelated history: %+v", got)
			}
			if tc.want != "" {
				var c result.Completion
				if err := json.Unmarshal(got.Output, &c); err != nil || c.State != tc.want || c.PolicyRevision != prepared.WorkMethodRevision {
					t.Fatalf("completion=%+v error=%v", c, err)
				}
				if tc.want == "done" && (len(c.Evidence) != 1 || c.Evidence[0].TurnID != "own") {
					t.Fatalf("lost normalized reference: %+v", c)
				}
			}
		})
	}
}
