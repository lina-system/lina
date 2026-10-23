// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// idleAdapter is a worker with no process: the registry only needs its event
// stream, and the closer decides when the session ends.
type idleAdapter struct {
	events chan Event
}

func newIdleAdapter() *idleAdapter { return &idleAdapter{events: make(chan Event)} }
func (*idleAdapter) Name() string  { return "idle" }
func (*idleAdapter) Version(context.Context) (string, error) {
	return "idle-1", nil
}
func (*idleAdapter) Capabilities() CapabilitySet { return nil }
func (*idleAdapter) Start(context.Context, lease.Lease, StartSpec) (Thread, error) {
	return Thread{}, errors.New("not startable")
}
func (*idleAdapter) Input(context.Context, Thread, Input) error { return errors.New("not startable") }
func (*idleAdapter) Interrupt(context.Context, Thread, Reason) error {
	return errors.New("not startable")
}
func (a *idleAdapter) Events() <-chan Event { return a.events }
func (*idleAdapter) Answer(context.Context, Thread, Answer) error {
	return errors.New("not startable")
}
func (*idleAdapter) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (*idleAdapter) Snapshot(context.Context, Thread) (ThreadSnapshot, error) {
	return ThreadSnapshot{}, errors.New("not startable")
}

type closer struct {
	mu     sync.Mutex
	closed []string
	err    error
}

func (c *closer) close(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = append(c.closed, "end")
	return c.err
}
func (c *closer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.closed)
}

func session(id, task string) Session {
	return Session{ID: id, Worker: "idle", Started: time.Unix(1800000000, 0), Thread: Thread{ID: "thread-" + id, TaskID: task, AttemptID: "attempt-" + id}}
}

func adopt(t *testing.T, l *Live, s Session, c *closer) {
	t.Helper()
	if err := l.Adopt(s, newIdleAdapter(), c.close); err != nil {
		t.Fatal(err)
	}
}

func TestEndKeepsTheThreadReadable(t *testing.T) {
	l := NewLive()
	c := &closer{}
	adopt(t, l, session("action-a", "task-a"), c)
	if got := l.Count(); got != 1 {
		t.Fatalf("live count = %d", got)
	}
	if err := l.End(t.Context(), "action-a"); err != nil {
		t.Fatal(err)
	}
	if l.Count() != 0 || c.count() != 1 {
		t.Fatalf("session not ended: live=%d closes=%d", l.Count(), c.count())
	}
	ended, ok := l.Ended("action-a")
	if !ok || ended.Thread.ID != "thread-action-a" || ended.Thread.TaskID != "task-a" || ended.Worker != "idle" || ended.Started.IsZero() {
		t.Fatalf("ended thread not readable: %+v %v", ended, ok)
	}
	// A repeated end is harmless and never closes the process twice.
	if err := l.End(t.Context(), "action-a"); err != nil || c.count() != 1 {
		t.Fatalf("repeated end: %v closes=%d", err, c.count())
	}
}

func TestEndUnlistedFollowsTheFrontier(t *testing.T) {
	l := NewLive()
	a, b := &closer{}, &closer{}
	adopt(t, l, session("action-a", "task-a"), a)
	adopt(t, l, session("action-b", "task-b"), b)
	if err := l.EndUnlisted(t.Context(), map[string]bool{"action-b": true}); err != nil {
		t.Fatal(err)
	}
	if l.Count() != 1 || a.count() != 1 || b.count() != 0 {
		t.Fatalf("frontier reap: live=%d a=%d b=%d", l.Count(), a.count(), b.count())
	}
	list := l.List()
	if len(list) != 1 || list[0].ID != "action-b" {
		t.Fatalf("kept set = %+v", list)
	}
}

func TestEndTaskEndsTheEarlierAttempt(t *testing.T) {
	l := NewLive()
	first, other := &closer{}, &closer{}
	adopt(t, l, session("action-1", "task-a"), first)
	adopt(t, l, session("action-2", "task-b"), other)
	if err := l.EndTask(t.Context(), "task-a"); err != nil {
		t.Fatal(err)
	}
	if first.count() != 1 || other.count() != 0 || l.Count() != 1 {
		t.Fatalf("task reap: first=%d other=%d live=%d", first.count(), other.count(), l.Count())
	}
}

func TestFailedJoinStaysLiveForAnotherAttempt(t *testing.T) {
	l := NewLive()
	c := &closer{err: errors.New("process did not join")}
	adopt(t, l, session("action-a", "task-a"), c)
	if err := l.EndUnlisted(t.Context(), nil); err == nil {
		t.Fatal("a failed join reported the session as ended")
	}
	if l.Count() != 1 {
		t.Fatalf("failed join dropped the session: %d", l.Count())
	}
	c.mu.Lock()
	c.err = nil
	c.mu.Unlock()
	if err := l.EndUnlisted(t.Context(), nil); err != nil || l.Count() != 0 {
		t.Fatalf("retry did not end the session: %v %d", err, l.Count())
	}
}

func TestExitEndsItsOwnSessionAndKeepsTheRecord(t *testing.T) {
	l := NewLive()
	c := &closer{}
	a := newIdleAdapter()
	if err := l.Adopt(session("action-a", "task-a"), a, c.close); err != nil {
		t.Fatal(err)
	}
	close(a.events)
	deadline := time.Now().Add(5 * time.Second)
	for l.Count() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if l.Count() != 0 || c.count() != 1 {
		t.Fatalf("exited process kept its session: live=%d closes=%d", l.Count(), c.count())
	}
	if _, ok := l.Ended("action-a"); !ok {
		t.Fatal("exited session lost its thread record")
	}
}

func TestAdoptRefusesIncompleteSessions(t *testing.T) {
	l := NewLive()
	c := &closer{}
	if err := l.Adopt(Session{}, newIdleAdapter(), c.close); !errors.Is(err, ErrSession) {
		t.Fatalf("empty session admitted: %v", err)
	}
	if err := l.Adopt(session("action-a", "task-a"), nil, c.close); !errors.Is(err, ErrSession) {
		t.Fatalf("missing adapter admitted: %v", err)
	}
	if err := l.Adopt(session("action-a", "task-a"), newIdleAdapter(), nil); !errors.Is(err, ErrSession) {
		t.Fatalf("missing close admitted: %v", err)
	}
	if l.Count() != 0 {
		t.Fatalf("refused sessions counted: %d", l.Count())
	}
}
