// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"testing"
	"time"
)

func authorizationStep(t *testing.T, e *Engine) State {
	t.Helper()
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	select {
	case err := <-e.failures:
		t.Fatal(err)
	default:
	}
	return revisionSnapshot(t, e)
}

func TestPublishedAuthorizationWaitResumesWithoutObservingAnEffect(t *testing.T) {
	t.Parallel()
	e := revisionEngine(t, readyTask("a"))
	granted := false
	effects, observations := 0, 0
	e.cfg.Driver = &fixtureDriver{authorize: func(Action) error {
		if !granted {
			return ErrNeedsPerson
		}
		return nil
	}, run: func(Action) (Observation, error) {
		effects++
		return Observation{Status: OutcomeRunning, Handle: "worker"}, nil
	}, observe: func(Action) (Observation, error) {
		observations++
		return Observation{Status: OutcomeUnknown}, nil
	}}
	a := authorizationStep(t, e).Actions[0]
	if a.State != "awaiting_authorization" {
		t.Fatal("fixture did not park before execution")
	}
	// Dev's published journal predates input tokens and refusal receipts.
	a.InputVersion, a.Receipt = nil, nil
	if err := e.journal.Update(t.Context(), Transition{ID: "published-wait", Kind: "fixture", At: 100}, func(v *View) error {
		return put(v, "action", a.ID, a)
	}); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Epoch++
	var err error
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = authorizationStep(t, e)
	granted = true
	if err := e.Signal(t.Context(), Signal{ID: "approval-after-upgrade", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	s := authorizationStep(t, e)
	if effects != 1 || observations != 0 || len(s.Actions) != 1 || s.Actions[0].ID != a.ID || s.Actions[0].State != "running" {
		t.Fatalf("published approval wait was treated as an executed effect: effects=%d observations=%d actions=%+v", effects, observations, s.Actions)
	}
}

func TestObservationDoesNotUseClaimSnapshotAfterAuthorizationRequeue(t *testing.T) {
	t.Parallel()
	e := revisionEngine(t, readyTask("a"))
	queued := revisionAction(t, e, "a", Assign)
	if err := e.Signal(t.Context(), Signal{ID: "first-approval-hint", ActionID: queued.ID}); err != nil {
		t.Fatal(err)
	}
	a, err := e.Claim(t.Context(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	observations := 0
	e.cfg.Driver = &fixtureDriver{observe: func(Action) (Observation, error) {
		observations++
		return Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true}, nil
	}}
	// The snapshot still says claimed, but its dispatch unwinds and approval
	// requeues it before the observer acquires the running reservation.
	e.cfg.Changed = func(ctx context.Context, _ State) error {
		if err := e.awaitAuthorization(ctx, a); err != nil {
			return err
		}
		return e.Signal(ctx, Signal{ID: "next-approval-hint", ActionID: a.ID})
	}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	select {
	case err := <-e.failures:
		t.Fatalf("obsolete claim observation stopped engine: %v", err)
	default:
	}
	if observations != 0 || revisionSnapshot(t, e).Actions[0].State != "queued" {
		t.Fatalf("observed an undispatched authorization retry: calls=%d", observations)
	}
}

func TestAuthorizationSignalRechecksGrantAndExecutesOnceAcrossRestart(t *testing.T) {
	t.Parallel()
	e := revisionEngine(t, readyTask("a"))
	checks, effects := 0, 0
	granted := false
	e.cfg.Driver = &fixtureDriver{
		authorize: func(Action) error {
			checks++
			if !granted {
				return ErrNeedsPerson
			}
			return nil
		},
		run: func(Action) (Observation, error) {
			effects++
			return Observation{Status: OutcomeRunning, Handle: "worker"}, nil
		},
		observe: func(Action) (Observation, error) {
			t.Error("undispatched authorization wait was observed as an effect")
			return Observation{}, ErrInvalid
		},
	}
	s := authorizationStep(t, e)
	a := s.Actions[0]
	if a.State != "awaiting_authorization" || effects != 0 || checks != 1 {
		t.Fatalf("authorization wait is not distinguished from execution: %+v checks=%d effects=%d", a, checks, effects)
	}
	// A hint is not approval. Retry only the authority check while still denied.
	if err := e.Signal(t.Context(), Signal{ID: "grant-still-denied", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	s = authorizationStep(t, e)
	if s.Actions[0].State != "awaiting_authorization" || checks != 2 || effects != 0 {
		t.Fatal("change hint granted authority or failed to recheck it")
	}
	cfg := e.cfg
	cfg.Epoch++
	var err error
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s = authorizationStep(t, e)
	if checks != 2 || effects != 0 || s.Goals[0].State != GoalBlocked || s.Goals[0].AttentionID == "" {
		t.Fatal("restart lost or replayed the authorization wait")
	}
	granted = true
	signal := Signal{ID: "grant-approved", ActionID: a.ID}
	if err = e.Signal(t.Context(), signal); err != nil {
		t.Fatal(err)
	}
	s = authorizationStep(t, e)
	if checks != 3 || effects != 1 || s.Actions[0].ID != a.ID || s.Actions[0].State != "running" || s.Goals[0].Tasks[0].Attempt != 1 {
		t.Fatalf("approved action did not resume once: %+v checks=%d effects=%d", s, checks, effects)
	}
	if err = e.Signal(t.Context(), signal); err != nil {
		t.Fatal(err)
	}
	_ = authorizationStep(t, e)
	if checks != 3 || effects != 1 {
		t.Fatal("duplicate approval signal repeated dispatch")
	}
}

func TestRevisionRetiresAuthorizationWaitWithoutDispatch(t *testing.T) {
	t.Parallel()
	for _, inFlight := range []bool{false, true} {
		t.Run(map[bool]string{false: "parked", true: "authorization-in-flight"}[inFlight], func(t *testing.T) {
			e := revisionEngine(t, readyTask("a"))
			entered, release := make(chan struct{}), make(chan struct{})
			e.cfg.Driver = &fixtureDriver{authorize: func(Action) error {
				close(entered)
				<-release
				return ErrNeedsPerson
			}}
			if err := e.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			<-entered
			if inFlight {
				revisionEdit(t, e)
			}
			close(release)
			e.wg.Wait()
			if !inFlight {
				revisionEdit(t, e)
			}
			s := revisionSnapshot(t, e)
			if s.Actions[0].State != "finished" || s.Goals[0].Tasks[0].Stage != TaskReady || s.Goals[0].Tasks[0].Attempt != 0 {
				t.Fatalf("revision retained obsolete authorization reservation: %+v", s)
			}
			if err := e.Signal(t.Context(), Signal{ID: "late-approval", ActionID: s.Actions[0].ID}); err != nil {
				t.Fatal(err)
			}
			if revisionSnapshot(t, e).Actions[0].State != "finished" {
				t.Fatal("late approval revived obsolete work")
			}
		})
	}
}

func TestSignalDoesNotRepeatExecutedActionNeedingPerson(t *testing.T) {
	t.Parallel()
	e := revisionEngine(t, readyTask("a"))
	effects := 0
	e.cfg.Driver = &fixtureDriver{run: func(Action) (Observation, error) {
		effects++
		return Observation{Status: OutcomeNeedsPerson}, nil
	}}
	s := authorizationStep(t, e)
	a := s.Actions[0]
	if a.State != "needs_person" {
		t.Fatal("executed action lost its outcome")
	}
	if err := e.Signal(t.Context(), Signal{ID: "person-answered", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	_ = authorizationStep(t, e)
	if effects != 1 {
		t.Fatal("person signal repeated an effect that was already dispatched")
	}
}

func TestAuthorizationSignalDuringCheckIsNotLost(t *testing.T) {
	t.Parallel()
	e := revisionEngine(t, readyTask("a"))
	entered, release := make(chan struct{}), make(chan struct{})
	checks, effects := 0, 0
	e.cfg.Driver = &fixtureDriver{authorize: func(Action) error {
		checks++
		if checks == 1 {
			close(entered)
			<-release
			return ErrNeedsPerson
		}
		return nil
	}, run: func(Action) (Observation, error) {
		effects++
		return Observation{Status: OutcomeRunning, Handle: "worker"}, nil
	}}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-entered
	a := revisionSnapshot(t, e).Actions[0]
	if err := e.Signal(t.Context(), Signal{ID: "approval-during-check", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	close(release)
	e.wg.Wait()
	s := authorizationStep(t, e)
	if checks != 2 || effects != 1 || s.Actions[0].State != "running" {
		t.Fatalf("in-flight approval wake was lost: checks=%d effects=%d action=%+v", checks, effects, s.Actions[0])
	}
}

func TestReleasedAuthorizationRetryWakesAfterEarlierSignalWasConsumed(t *testing.T) {
	t.Parallel()
	e := revisionEngine(t, readyTask("a"))
	a := revisionAction(t, e, "a", Assign)
	// An approval hint has requeued this identity while its previous authority
	// check is still unwinding. Hold that dispatch slot until after a complete
	// reconciliation has consumed the hint; no timing-based interleaving needed.
	e.running[a.ID] = true
	dispatched := make(chan struct{}, 1)
	e.cfg.Driver = &fixtureDriver{run: func(Action) (Observation, error) {
		dispatched <- struct{}{}
		return Observation{Status: OutcomeRunning, Handle: "worker"}, nil
	}}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	for len(e.journal.wake) > 0 {
		<-e.journal.Wake()
	}
	stepped := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	r := startReconciler(ctx, e.journal, func(ctx context.Context) error {
		err := e.step(ctx)
		select {
		case stepped <- struct{}{}:
		default:
		}
		return err
	}, make(chan time.Time), nil) // No sweep can rescue a lost wake.
	defer func() { cancel(); <-r.Done(); e.wg.Wait() }()
	select {
	case <-stepped:
	case <-ctx.Done():
		t.Fatal("initial reconciliation did not finish")
	}
	e.releaseDispatch(a.ID)
	select {
	case <-dispatched:
	case <-ctx.Done():
		t.Fatal("ready authorization retry required a sweep after dispatch released")
	}
}
