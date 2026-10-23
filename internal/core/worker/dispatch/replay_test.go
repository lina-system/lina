// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestDispatchReplaysDurableAdmissionBeforeManager(t *testing.T) {
	for _, state := range []string{"queued", "unknown", "finished"} {
		t.Run(state, func(t *testing.T) {
			db := store(t)
			origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"}
			d := definition(t)
			saved := Record{ID: admissionID(origin, "call"), Origin: origin, Definition: d, State: state, Owner: "dev-owner", Manager: "manager:original"}
			if state != "queued" {
				// Execution may already have assigned a branch to a legacy record.
				saved.Branch = "task-existing-assignment"
				saved.Code = "interrupted_execution"
			}
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", saved.ID, origin.Turn, d.Repository, state, mustJSON(saved))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			manages := 0
			s, err := Open(t.Context(), Config{DB: db, Epoch: 1, Manage: func(_ context.Context, r Record) (Record, error) {
				manages++
				return r, tools.ErrKeyConflict
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(context.Background())
			call := provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(mustJSON(d))}
			tool := s.Tool(origin)
			if _, err = tool.Validate(call); err != nil {
				t.Fatal(err)
			}
			result := tool.Execute(t.Context(), call)
			if result.Code != loop.OK {
				t.Fatalf("durable admission denied on retry: code=%s output=%s manager_calls=%d", result.Code, result.Output, manages)
			}
			var receipt struct{ ID, Branch, Owner, Manager string }
			if err = json.Unmarshal(result.Output, &receipt); err != nil || receipt.ID != saved.ID || receipt.Branch != saved.TaskDefinition().Branch || receipt.Owner != saved.Owner || receipt.Manager != saved.Manager || manages != 0 {
				t.Fatalf("replay changed admission: %s manager_calls=%d err=%v", result.Output, manages, err)
			}
			d.Done = "different task"
			call.Arguments = string(mustJSON(d))
			if tool.Execute(t.Context(), call).Code != loop.Denied {
				t.Fatal("replayed changed definition")
			}
			call.Arguments = string(mustJSON(saved.Definition))
			origin.Agent = "other-agent"
			if s.Tool(origin).Execute(t.Context(), call).Code != loop.Denied {
				t.Fatal("replayed changed origin")
			}
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0], saved) || manages != 0 {
				t.Fatalf("retry mutated durable state: %+v manages=%d err=%v", records, manages, err)
			}
		})
	}
}
