// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestInputAdmissionPrecedesDispatch(t *testing.T) {
	for _, mode := range []string{"steer", "continue", "refuse_steer", "refuse_continue", "complete_during_admission"} {
		t.Run(mode, func(t *testing.T) {
			p := newAdapterPeer(t, `{"rateLimits":{}}`)
			th := p.start(t)
			p.receivedMethod(t, "turn/start")
			complete := func() {
				t.Helper()
				p.send(frame{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn-1","status":"completed","items":[]}}`)})
				for {
					select {
					case event := <-p.s.Events():
						if event.Kind == worker.FinishEvent {
							return
						}
					case <-p.ctx.Done():
						t.Fatal(p.ctx.Err())
					}
				}
			}
			steering := mode != "continue" && mode != "refuse_continue"
			if !steering {
				complete()
			}
			adapter, ok := any(p.s).(interface {
				InputWithAdmission(context.Context, worker.Thread, worker.Input, func(steering bool) error) error
			})
			if !ok {
				t.Fatal("adapter cannot admit the actual input kind before dispatch")
			}
			calls := 0
			refusal := errors.New("admission persistence failed")
			err := adapter.InputWithAdmission(p.ctx, th, worker.Input{Text: "Continue the work"}, func(actual bool) error {
				calls++
				if actual != steering {
					t.Errorf("steering=%v want=%v", actual, steering)
				}
				select {
				case request := <-p.received:
					t.Fatalf("request before admission: %s", request.Method)
				default:
				}
				if mode == "refuse_steer" || mode == "refuse_continue" {
					return refusal
				}
				if mode == "complete_during_admission" {
					complete()
				}
				return nil
			})
			if calls != 1 {
				t.Fatalf("admission calls=%d", calls)
			}
			if mode == "refuse_steer" || mode == "refuse_continue" || mode == "complete_during_admission" {
				if !errors.Is(err, worker.ErrInputRefused) {
					t.Fatalf("undispatched input not refused: %v", err)
				}
				if mode != "complete_during_admission" && !errors.Is(err, refusal) {
					t.Fatal("lost admission failure", err)
				}
				select {
				case request := <-p.received:
					t.Fatalf("refused input dispatched: %s", request.Method)
				default:
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				method := "turn/start"
				if steering {
					method = "turn/steer"
				}
				p.receivedMethod(t, method)
			}
		})
	}
}
