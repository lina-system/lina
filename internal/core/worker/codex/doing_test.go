// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/worker"
	"testing"
)

func TestDoingFromNativePlanAndTitle(t *testing.T) {
	s := &Session{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, State: worker.Active}, events: make(chan worker.Event, 257), finished: map[string]bool{}}
	for i, raw := range []string{
		`{"method":"thread/name/updated","params":{"threadId":"thread","threadName":"Login repair"}}`,
		`{"method":"turn/plan/updated","params":{"threadId":"thread","turnId":"turn","plan":[{"step":"Running tests","status":"inProgress"}]}}`,
	} {
		if err := s.notification([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-s.events:
			b, _ := json.Marshal(e)
			var value struct {
				Doing *struct {
					Line string
					Busy bool
				}
			}
			json.Unmarshal(b, &value)
			if value.Doing == nil || value.Doing.Line != []string{"Login repair", "Login repair · Running tests"}[i] || !value.Doing.Busy {
				t.Fatalf("missing doing: %s", b)
			}
		default:
			t.Fatal("title/plan event discarded")
		}
	}
	if err := s.notification([]byte(`{"method":"turn/plan/updated","params":{"threadId":"thread","turnId":"old","plan":[{"step":"stale","status":"inProgress"}]}}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-s.events:
		t.Fatalf("stale plan published: %+v", e)
	default:
	}
}

func TestDoingMetadataBeforeFirstTurnDoesNotPublishUnboundEvent(t *testing.T) {
	s := &Session{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread"}, State: worker.Idle}, events: make(chan worker.Event, 257), finished: map[string]bool{}}
	if err := s.notification([]byte(`{"method":"thread/name/updated","params":{"threadId":"thread","threadName":"Login"}}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-s.events:
		t.Fatalf("metadata published before turn binding: %+v", e)
	default:
	}
	if s.doing.Snapshot().Line != "Login" {
		t.Fatal("early title lost")
	}
}
