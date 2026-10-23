// SPDX-License-Identifier: Apache-2.0
package seat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/grant"
)

type journal struct {
	receipts []Receipt
	fail     error
}

func (j *journal) Append(r Receipt) error {
	if j.fail != nil {
		return j.fail
	}
	j.receipts = append(j.receipts, r)
	return nil
}
func fixture(task, session string) (grant.Grant, grant.Request, grant.Environment, Consent) {
	r := grant.Request{GrantID: "grant-" + task + "-" + session, MainID: "main", NodeID: "node", NodeBootID: "boot", Epoch: 1, TaskID: task, CommandID: "command", Host: "host", OSUser: "user", GUISession: session, Person: "person", Agent: "agent", Tool: "gui", Target: session}
	now := time.Unix(100, 0)
	g := grant.Grant{Identity: r, Expires: now.Add(time.Hour), Scope: []grant.Permission{{Tool: r.Tool, Target: r.Target}}}
	return g, r, grant.Environment{Epoch: 1, Now: now, OSPermitted: true, LiveSession: session}, Consent{Revision: 1, GrantID: r.GrantID, Person: r.Person, TaskID: task, Session: session}
}
func acquire(t *testing.T, m *Manager, task, session string) *Lease {
	t.Helper()
	g, r, e, c := fixture(task, session)
	l, err := m.Acquire(g, r, e, c)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func TestExplicitExclusiveHandover(t *testing.T) {
	j := &journal{}
	m := New(j)
	g, r, e, c := fixture("first", "seat")
	if _, err := m.Acquire(g, r, e, Consent{}); !errors.Is(err, ErrConsent) {
		t.Fatalf("missing consent: %v", err)
	}
	first := acquire(t, m, "first", "seat")
	g2, r2, e2, c2 := fixture("second", "seat")
	if _, err := m.Acquire(g2, r2, e2, c2); !errors.Is(err, ErrBusy) {
		t.Fatalf("second task: %v", err)
	}
	acquire(t, m, "second", "other-seat")
	receipts, err := m.Handle(Event{Kind: HumanInput, Session: "seat"})
	if err != nil || len(receipts) != 1 || receipts[0].ID == "" || receipts[0].GrantID != r.GrantID || receipts[0].TaskID != r.TaskID || receipts[0].AgentID != r.Agent {
		t.Fatalf("end receipt: %+v %v", receipts, err)
	}
	if first.Context().Err() != context.Canceled {
		t.Fatal("human input did not cancel work")
	}
	if err := first.Check(r, e); !errors.Is(err, ErrEnded) {
		t.Fatalf("stale effect: %v", err)
	}
	if _, err := m.Acquire(g, r, e, c); !errors.Is(err, ErrEnded) {
		t.Fatalf("stale grant reacquired: %v", err)
	}
	c2.Revision = m.Revision("seat") // A new explicit handover after the person took control.
	if _, err := m.Acquire(g2, r2, e2, c2); err != nil {
		t.Fatalf("waiting task cannot acquire released seat: %v", err)
	}
	if len(j.receipts) != 1 {
		t.Fatalf("journal: %+v", j.receipts)
	}
}
func TestLockUnlockNeverRevivesLease(t *testing.T) {
	j := &journal{}
	m := New(j)
	l := acquire(t, m, "task", "seat")
	if _, err := m.Handle(Event{Kind: Locked, Session: "seat"}); err != nil {
		t.Fatal(err)
	}
	g, r, e, c := fixture("fresh", "seat")
	c.Revision = m.Revision("seat")
	if _, err := m.Acquire(g, r, e, c); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v", err)
	}
	if _, err := m.Handle(Event{Kind: Unlocked, Session: "seat"}); err != nil {
		t.Fatal(err)
	}
	if l.Context().Err() != context.Canceled {
		t.Fatal("unlock revived context")
	}
	old, oldr, olde, oldc := fixture("task", "seat")
	if _, err := m.Acquire(old, oldr, olde, oldc); !errors.Is(err, ErrEnded) {
		t.Fatalf("unlock revived grant: %v", err)
	}
	if _, err := m.Acquire(g, r, e, c); err != nil {
		t.Fatal(err)
	}
	if len(j.receipts) != 1 || j.receipts[0].Reason != Locked {
		t.Fatalf("lock receipt: %+v", j.receipts)
	}
}
func TestResumeRevokesEverySession(t *testing.T) {
	j := &journal{}
	m := New(j)
	a := acquire(t, m, "a", "one")
	b := acquire(t, m, "b", "two")
	receipts, err := m.Handle(Event{Kind: Resumed})
	if err != nil || len(receipts) != 2 || a.Context().Err() != context.Canceled || b.Context().Err() != context.Canceled {
		t.Fatalf("resume: %+v %v", receipts, err)
	}
	for _, r := range receipts {
		if r.ID == "" || r.Reason != Resumed {
			t.Fatalf("receipt: %+v", r)
		}
	}
	if receipts[0].ID == receipts[1].ID {
		t.Fatal("duplicate receipt ids")
	}
	again, err := m.Handle(Event{Kind: Resumed})
	if err != nil || len(again) != 0 || len(j.receipts) != 2 {
		t.Fatalf("duplicate event: %+v %v", again, err)
	}
}
func TestReceiptFailureStillStopsAllWork(t *testing.T) {
	j := &journal{fail: errors.New("disk unavailable")}
	m := New(j)
	a := acquire(t, m, "a", "one")
	b := acquire(t, m, "b", "two")
	if _, err := m.Handle(Event{Kind: Resumed}); !errors.Is(err, j.fail) {
		t.Fatalf("lost journal failure: %v", err)
	}
	if a.Context().Err() != context.Canceled || b.Context().Err() != context.Canceled {
		t.Fatal("journal failure left work live")
	}
	g, r, e, c := fixture("fresh", "one")
	if _, err := m.Acquire(g, r, e, c); !errors.Is(err, ErrJournal) {
		t.Fatalf("unsafe new lease: %v", err)
	}
}
func TestGrantIsCheckedAtEveryEffect(t *testing.T) {
	j := &journal{}
	m := New(j)
	l := acquire(t, m, "a", "seat")
	_, r, e, _ := fixture("a", "seat")
	r.TaskID = "other"
	if err := l.Check(r, e); !errors.Is(err, grant.ErrTask) {
		t.Fatalf("foreign task: %v", err)
	}
	_, r, e, _ = fixture("a", "seat")
	e.Now = e.Now.Add(time.Hour)
	if err := l.Check(r, e); !errors.Is(err, grant.ErrExpired) {
		t.Fatalf("expired grant: %v", err)
	}
	if l.Context().Err() != context.Canceled || len(j.receipts) != 1 || j.receipts[0].Reason != Expired {
		t.Fatal("expiry did not end lease")
	}
}
func TestConcurrentTasksCannotShareSeat(t *testing.T) {
	m := New(&journal{})
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, task := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			g, r, e, c := fixture(task, "seat")
			_, err := m.Acquire(g, r, e, c)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes, busy := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrBusy) {
			busy++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("success=%d busy=%d", successes, busy)
	}
}

func TestOldUnclaimedConsentIsFencedByTakeover(t *testing.T) {
	for _, kind := range []Kind{HumanInput, Locked, Resumed} {
		t.Run(string(kind), func(t *testing.T) {
			m := New(&journal{})
			g, r, e, c := fixture("waiting", "seat")
			c.Revision = m.Revision("seat")
			event := Event{Kind: kind, Session: "seat"}
			if kind == Resumed {
				event.Session = ""
			}
			if _, err := m.Handle(event); err != nil {
				t.Fatal(err)
			}
			if kind == Locked {
				if _, err := m.Handle(Event{Kind: Unlocked, Session: "seat"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.Acquire(g, r, e, c); !errors.Is(err, ErrConsent) {
				t.Fatalf("pre-takeover consent accepted: %v", err)
			}
			c.Revision = m.Revision("seat")
			if _, err := m.Acquire(g, r, e, c); err != nil {
				t.Fatalf("fresh explicit handover: %v", err)
			}
		})
	}
}

func TestGrantMutationCannotWidenLease(t *testing.T) {
	m := New(&journal{})
	g, r, e, c := fixture("a", "seat")
	l, err := m.Acquire(g, r, e, c)
	if err != nil {
		t.Fatal(err)
	}
	g.Scope[0].Target = "outside"
	r.Target = "outside"
	if err := l.Check(r, e); !errors.Is(err, grant.ErrScope) {
		t.Fatalf("mutable grant widened scope: %v", err)
	}
}

type blockingJournal struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (j *blockingJournal) Append(Receipt) error {
	j.once.Do(func() { close(j.entered) })
	<-j.release
	return nil
}
func TestSlowReceiptDoesNotDelayAnotherSessionsTakeover(t *testing.T) {
	j := &blockingJournal{entered: make(chan struct{}), release: make(chan struct{})}
	m := New(j)
	acquire(t, m, "first", "one")
	second := acquire(t, m, "second", "two")
	var wg sync.WaitGroup
	t.Cleanup(func() { close(j.release); wg.Wait() })
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = m.Handle(Event{Kind: HumanInput, Session: "one"}) }()
	<-j.entered // First receipt is blocked on storage, with no timing assumption.
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = m.Handle(Event{Kind: HumanInput, Session: "two"}) }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-second.Context().Done():
	case <-ctx.Done():
		t.Fatal("slow journal prevented takeover on another session")
	}
}
