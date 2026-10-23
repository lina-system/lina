// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestFailedTurnClassification(t *testing.T) {
	for _, tc := range []struct {
		info string
		want worker.FailureKind
	}{
		{`"usageLimitExceeded"`, worker.QuotaFailure},
		{`"sessionBudgetExceeded"`, worker.QuotaFailure},
		{`"rateLimitExceeded"`, worker.RateLimitFailure},
		{`"unauthorized"`, worker.LoginFailure},
		{`{"httpConnectionFailed":{"httpStatusCode":401}}`, worker.LoginFailure},
		{`{"responseStreamDisconnected":{"httpStatusCode":429}}`, worker.RateLimitFailure},
		{`{"responseStreamConnectionFailed":{"httpStatusCode":503}}`, worker.ConnectionFailure},
		{`{"httpConnectionFailed":{}}`, worker.ConnectionFailure},
		{`{"responseStreamDisconnected":{}}`, worker.ConnectionFailure},
		{`"futureError"`, worker.WorkerFailure},
	} {
		t.Run(tc.info, func(t *testing.T) {
			s := &Session{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, State: worker.Active}, events: make(chan worker.Event, 257), finished: map[string]bool{}}
			raw := json.RawMessage(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"failed","error":{"message":"private failure detail","codexErrorInfo":` + tc.info + `}}}}`)
			if err := s.notification(raw); err != nil {
				t.Fatal(err)
			}
			got := <-s.events
			if got.Finish.Failure != tc.want || got.Finish.Code == "private failure detail" {
				t.Fatalf("finish=%+v", got.Finish)
			}
		})
	}
}

func TestCompletionBeforeStartReplyRetainsTurnBinding(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			completion := frame{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn-1","status":"` + status + `","error":{"message":"quota","codexErrorInfo":"usageLimitExceeded"}}}`)}
			p := newAdapterPeerTurnScript(t, `{"rateLimits":{}}`, []frame{completion}, nil, false)
			thread := p.start(t)
			if thread.TurnID != "turn-1" {
				t.Fatalf("early completion lost accepted turn binding: %+v", thread)
			}
			select {
			case event := <-p.s.Events():
				if event.Kind != worker.FinishEvent || event.Thread != thread || event.Validate() != nil {
					t.Fatalf("completion differs from accepted worker: %+v", event)
				}
				if status == "failed" && event.Finish.Failure != worker.QuotaFailure {
					t.Fatalf("quota completion lost: %+v", event.Finish)
				}
			case <-p.ctx.Done():
				t.Fatal(p.ctx.Err())
			}
			p.s.mu.Lock()
			state := p.s.snapshot.State
			p.s.mu.Unlock()
			if state != worker.Idle {
				t.Fatalf("completed turn reopened: %s", state)
			}
		})
	}
}

func TestWorkPanelActivityFromItemStart(t *testing.T) {
	for _, tc := range []struct{ item, phase string }{
		{`{"type":"commandExecution","id":"cmd","command":"private command","cwd":"/private","processId":null,"status":"inProgress","commandActions":[],"aggregatedOutput":null,"exitCode":null,"durationMs":null}`, "command"},
		{`{"type":"fileChange","id":"file","changes":[],"status":"inProgress"}`, "file_change"},
	} {
		s := &Session{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, State: worker.Active}, events: make(chan worker.Event, 257), finished: map[string]bool{}}
		if err := s.notification([]byte(`{"method":"item/started","params":{"threadId":"thread","turnId":"turn","item":` + tc.item + `}}`)); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-s.events:
			if event.Progress == nil || event.Progress.Phase != tc.phase || event.Progress.Text != "" {
				t.Fatalf("missing or unsafe activity: %+v", event.Progress)
			}
		default:
			t.Fatal("item activity was discarded")
		}
	}
}
