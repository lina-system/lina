// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestBridgedDispatchRequiresCommittedSuccessfulReceipt(t *testing.T) {
	for _, mode := range []string{"direct", "bridge", "uncommitted", "denied", "wrong tool", "malformed", "wrong call", "wrong receipt"} {
		t.Run(mode, func(t *testing.T) {
			db := store(t)
			origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"}
			row, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: origin.Conversation, Source: "fixture", NativeID: "input"}, Body: []byte("request")})
			if err != nil {
				t.Fatal(err)
			}
			record := Record{ID: admissionID(origin, "dispatch"), Origin: origin, State: "queued", Definition: Definition{Repository: "fixture"}}
			call := provider.ToolCall{ID: "dispatch", Name: "call_tool", Arguments: `{"name":"dispatch_work","arguments":{}}`}
			result := loop.Result{ID: call.ID, Code: loop.OK, Output: mustJSON(map[string]string{"id": record.ID})}
			switch mode {
			case "direct":
				call.Name = "dispatch_work"
				call.Arguments = `{}`
			case "denied":
				result.Code = loop.Denied
			case "wrong tool":
				call.Arguments = `{"name":"worker_start","arguments":{}}`
			case "malformed":
				call.Arguments = `not json`
			case "wrong call":
				call.ID = "other"
			case "wrong receipt":
				result.Output = mustJSON(map[string]string{"id": "other"})
			}
			var output any = mustJSON(result)
			if mode == "uncommitted" {
				output = nil
			}
			if err = db.Write(t.Context(), func(tx *canon.Tx) error {
				if _, err := tx.ExecContext(t.Context(), "INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES(?,?,?,'active')", origin.Turn, origin.Conversation, row.ID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,?,?,?)", origin.Turn, call.ID, mustJSON(call), output); err != nil {
					return err
				}
				_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", record.ID, origin.Turn, "fixture", record.State, mustJSON(record))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Run admission synchronously with a saturated host: a recognized receipt
			// advances to the real capacity gate, without scheduling or timing luck.
			claims := 0
			service := &Service{ctx: t.Context(), cfg: Config{DB: db, Capacity: func(context.Context) int { claims++; return 0 }}, running: map[string]bool{}}
			service.sync()
			records, err := service.records(t.Context())
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			want := mode == "direct" || mode == "bridge"
			if (claims == 1) != want || (records[0].Wait == "host_capacity") != want {
				t.Fatalf("%s: receipt reached worker admission=%d, wait=%s; want %v", mode, claims, records[0].Wait, want)
			}
		})
	}
}
