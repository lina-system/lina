// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
	"testing"
	"time"
)

func TestPlanAndTitleReachSameACPEvent(t *testing.T) {
	a := New(Config{})
	a.life = context.Background()
	a.state = worker.Active
	th := worker.Thread{ID: "session", TurnID: "turn"}
	var since time.Time
	for i, raw := range []string{
		`{"sessionUpdate":"session_info_update","title":"Login"}`,
		`{"sessionUpdate":"plan","entries":[{"content":"Running tests","status":"in_progress","priority":"medium"}]}`,
		`{"sessionUpdate":"plan","entries":[]}`,
	} {
		var u wire.Update
		u.Notification.SessionID = "session"
		if err := json.Unmarshal([]byte(raw), &u.Notification.Update); err != nil {
			t.Fatal(err)
		}
		if !a.update(th, u) {
			t.Fatal("update refused")
		}
		select {
		case e := <-a.Events():
			want := []string{"Login", "Login · Running tests", "Login"}[i]
			if e.Doing == nil || e.Doing.Line != want || !e.Doing.Busy {
				t.Fatalf("wrong observation: %+v", e.Doing)
			}
			if i == 0 {
				since = e.Doing.Since
			} else if !since.Equal(e.Doing.Since) {
				t.Fatal("busy time moved without a transition")
			}
		default:
			t.Fatal("observation delayed beyond adapter event")
		}
		u.Replay = true
		a.update(th, u)
		select {
		case <-a.Events():
			t.Fatal("load replay published as new progress")
		default:
		}
		u.Replay = false
		u.Notification.SessionID = "foreign"
		a.update(th, u)
		select {
		case <-a.Events():
			t.Fatal("foreign session progress published")
		default:
		}
	}
}
