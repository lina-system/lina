// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestWorkerAnswerClaimIsAtomicAndRetainsChoice(t *testing.T) {
	t.Parallel()
	for _, choice := range []authority.Choice{authority.Once, authority.Deny} {
		t.Run(string(choice), func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			a := requestWorker(t, f)
			if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: choice}); err != nil {
				t.Fatal(err)
			}
			type result struct {
				a   authority.Approval
				err error
			}
			start, results := make(chan struct{}), make(chan result, 8)
			for range cap(results) {
				go func() {
					<-start
					a, err := f.service.ClaimWorkerAnswer(t.Context(), a.ID)
					results <- result{a, err}
				}()
			}
			close(start)
			claimed := 0
			for range cap(results) {
				r := <-results
				if r.err == nil {
					claimed++
					if r.a.ID != a.ID || r.a.Choice != choice || r.a.State != authority.Consumed || r.a.AnsweredBy != "person" || r.a.Conversation != a.Conversation || r.a.AnsweredAt.IsZero() {
						t.Errorf("claimed answer: %+v", r.a)
					}
				} else if !errors.Is(r.err, authority.ErrStale) {
					t.Error(r.err)
				}
			}
			if claimed != 1 {
				t.Fatalf("claimed %d times", claimed)
			}
			stored, err := f.service.Approval(t.Context(), a.ID)
			if err != nil || stored.State != authority.Consumed || stored.Choice != choice {
				t.Fatalf("stored answer: %+v %v", stored, err)
			}
			if _, err := f.service.RequestWorkerApproval(t.Context(), workerApproval()); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("consumed request reopened: %v", err)
			}
		})
	}
}

func TestWorkerApprovalStopClosesUnclaimedStates(t *testing.T) {
	t.Parallel()
	for _, choice := range []authority.Choice{"", authority.Once, authority.Deny} {
		t.Run("choice="+string(choice), func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			a := requestWorker(t, f)
			if choice != "" {
				if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: choice}); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := f.service.StopWorkerApproval(t.Context(), a.ID); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := f.service.Approval(t.Context(), a.ID)
			if err != nil || stored.State != authority.Stopped || stored.Choice != choice {
				t.Fatalf("stopped answer: %+v %v", stored, err)
			}
			if _, err := f.service.ClaimWorkerAnswer(t.Context(), a.ID); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("stopped claim: %v", err)
			}
			if _, err := f.service.RequestWorkerApproval(t.Context(), workerApproval()); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("stopped request reopened: %v", err)
			}
			if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("late answer: %v", err)
			}
		})
	}
}

func TestWorkerApprovalRejectsNonRelayAndMissingRecords(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, "missing", ""} {
		if _, err := f.service.ClaimWorkerAnswer(t.Context(), id); !errors.Is(err, authority.ErrStale) {
			t.Fatalf("non-relay claim %q: %v", id, err)
		}
		if err := f.service.StopWorkerApproval(t.Context(), id); !errors.Is(err, authority.ErrStale) {
			t.Fatalf("non-relay stop %q: %v", id, err)
		}
	}
	in := workerApproval()
	in.EffectKey = f.call.Key
	if _, err := f.service.RequestWorkerApproval(t.Context(), in); !errors.Is(err, tools.ErrKeyConflict) {
		t.Fatalf("non-relay key reuse: %v", err)
	}
	stored, err := f.service.Approval(t.Context(), a.ID)
	if err != nil || stored.State != authority.Approved {
		t.Fatalf("non-relay changed: %+v %v", stored, err)
	}
}

func TestWorkerApprovalRestartStopsUnusedAnswers(t *testing.T) {
	t.Parallel()
	for _, choice := range []authority.Choice{"", authority.Once, authority.Deny} {
		t.Run("choice="+string(choice), func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			a := requestWorker(t, f)
			if choice != "" {
				if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: choice}); err != nil {
					t.Fatal(err)
				}
			}
			oldOwner := owner(f)
			if err := f.store.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.cfg.Epoch++
			var err error
			f.store, err = canon.Open(t.Context(), f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			f.open(t)
			stored, err := f.service.Approval(t.Context(), a.ID)
			if err != nil || (choice != authority.Deny && stored.State != authority.Stopped) {
				t.Fatalf("restart state: %+v %v", stored, err)
			}
			if _, err := f.service.ClaimWorkerAnswer(t.Context(), a.ID); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("old epoch claim: %v", err)
			}
			if _, err := f.service.RequestWorkerApproval(t.Context(), workerApproval()); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("old request reopened: %v", err)
			}
			if err := f.service.Answer(t.Context(), a.ID, oldOwner, authority.Decision{Choice: authority.Once}); !errors.Is(err, authority.ErrStale) {
				t.Fatalf("old epoch answer: %v", err)
			}
		})
	}
}

func TestWorkerApprovalCallbacksObserveCommitsAndClaimDoesNotReenter(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var changes int
	var id string
	wantState := authority.Pending
	var err error
	f.service, err = authority.Open(t.Context(), f.store, authority.Config{
		Epoch: f.cfg.Epoch, CanPresent: func() bool { return f.surfaceAvailable },
		Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
			t.Error("worker relay consulted model grant resolver")
			return grant.Grant{}, grant.Environment{}, tools.ErrInvalid
		},
		ApprovalsChanged: func(ctx context.Context) {
			changes++
			if id == "" {
				id = pending(t, f).ID
			}
			a, err := f.service.Approval(ctx, id)
			if err != nil || a.State != wantState {
				t.Errorf("callback before commit: %+v %v", a, err)
			}
			if a.State == authority.Approved {
				if _, err := f.service.ClaimWorkerAnswer(ctx, id); err != nil {
					t.Errorf("callback claim: %v", err)
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestWorker(t, f)
	requestWorker(t, f)
	if changes != 1 {
		t.Fatalf("request notifications: %d", changes)
	}
	wantState = authority.Stopped
	if err := f.service.StopWorkerApproval(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := f.service.StopWorkerApproval(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatalf("stop notifications: %d", changes)
	}
	in := workerApproval()
	in.EffectKey = "second"
	id, wantState = "", authority.Pending
	if _, err := f.service.RequestWorkerApproval(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	wantState = authority.Approved
	if err := f.service.Answer(t.Context(), id, owner(f), authority.Decision{Choice: authority.Once}); err != nil {
		t.Fatal(err)
	}
	if changes != 4 {
		t.Fatalf("claim reentered notification: %d", changes)
	}
	if err := f.service.StopWorkerApproval(t.Context(), id); !errors.Is(err, authority.ErrStale) {
		t.Fatalf("consumed answer stopped: %v", err)
	}
	if changes != 4 {
		t.Fatal("failed stop notified")
	}
}
