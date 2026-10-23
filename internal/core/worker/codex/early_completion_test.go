// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestStartRetainsCompletionBeforeTurnReply(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			completion := frame{Method: "turn/completed", Params: json.RawMessage(fmt.Sprintf(`{"threadId":"thread","turn":{"id":"turn-1","status":%q,"error":{"message":"quota","codexErrorInfo":"usageLimitExceeded"}}}`, status))}
			// The reader processes this completion before delivering the reply.
			// No turn/started notification fills in the binding on its behalf.
			p := newAdapterPeerTurnScript(t, `{"rateLimits":{}}`, []frame{completion}, nil, false)
			thread := p.start(t)
			if thread.ID != "thread" || thread.TurnID != "turn-1" {
				t.Fatalf("early completion lost the returned turn binding: %+v", thread)
			}
			select {
			case event := <-p.s.Events():
				if event.Kind != worker.FinishEvent || event.Thread != thread || event.Finish == nil {
					t.Fatalf("completion does not match the returned binding: %+v", event)
				}
				if status == "failed" && event.Finish.Failure != worker.QuotaFailure || status == "completed" && event.Finish.Status != worker.Done {
					t.Fatalf("completion classification changed: %+v", event.Finish)
				}
			case <-p.ctx.Done():
				t.Fatal("completion event missing")
			}
			p.s.mu.Lock()
			state := p.s.snapshot.State
			p.s.mu.Unlock()
			if state != worker.Idle {
				t.Fatalf("reply revived a completed turn: %s", state)
			}
		})
	}
}
