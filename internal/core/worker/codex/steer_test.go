// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestSteerNeverStartsIdleTurn(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{}}`)
	thread := p.start(t)
	if err := p.s.Steer(p.ctx, thread, worker.Input{Text: "include failure details"}); err != nil {
		t.Fatal(err)
	}
	p.receivedMethod(t, "turn/steer")
	p.send(frame{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn-1","status":"completed","items":[]}}`)})
	for {
		select {
		case event := <-p.s.Events():
			if event.Kind != worker.FinishEvent {
				continue
			}
			if event.Finish.Status != worker.Done {
				t.Fatalf("unexpected finish: %+v", event.Finish)
			}
		case <-p.ctx.Done():
			t.Fatal(p.ctx.Err())
		}
		break
	}
	if err := p.s.Steer(p.ctx, thread, worker.Input{Text: "late follow-up"}); err == nil {
		t.Fatal("steer reopened an idle turn")
	}
	select {
	case request := <-p.received:
		t.Fatalf("idle steering wrote another request: %+v", request)
	default:
	}
}
