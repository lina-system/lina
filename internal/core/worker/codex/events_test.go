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
