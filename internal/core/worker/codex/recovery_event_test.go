// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestTerminalReadDoesNotSuppressFinishNotification(t *testing.T) {
	for _, method := range []string{"snapshot", "reconcile"} {
		t.Run(method, func(t *testing.T) {
			s, ctx, l := openFixture(t, "recover-command-done")
			th, err := s.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "Produce a result"})
			if err != nil {
				t.Fatal(err)
			}
			var snap worker.ThreadSnapshot
			if method == "snapshot" {
				snap, err = s.Snapshot(ctx, th)
			} else {
				snap, err = s.Reconcile(ctx, th)
				if snap.Finish == nil || snap.Finish.Status != worker.Done || snap.EffectsKnown {
					t.Fatalf("command readback must retain uncertain effects: %+v", snap)
				}
			}
			if err != nil || snap.State != worker.Idle {
				t.Fatalf("terminal readback: %+v %v", snap, err)
			}
			raw, _ := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": th.ID, "turn": map[string]any{"id": th.TurnID, "status": "completed"}}})
			for range 2 {
				if err := s.notification(raw); err != nil {
					t.Fatal(err)
				}
			}
			finishes := 0
			for {
				select {
				case event := <-s.Events():
					if event.Finish != nil {
						if event.Thread != th || event.Finish.Status != worker.Done {
							t.Fatalf("wrong live completion: %+v", event)
						}
						finishes++
					}
				default:
					if finishes != 1 {
						t.Fatalf("terminal readback suppressed or duplicated live completion: got %d, want 1", finishes)
					}
					return
				}
			}
		})
	}
}
