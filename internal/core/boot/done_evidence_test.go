// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/loop"
)

func TestDoneEvidenceCannotUseForeignOrFailedObservations(t *testing.T) {
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
	observed, err := r.doneObservations(t.Context())
	if err != nil || len(observed) != 1 || !observed[[2]string{"own", "read"}] {
		t.Fatalf("crossed evidence scope: %+v %v", observed, err)
	}
}
