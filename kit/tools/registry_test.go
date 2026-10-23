// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
)

type testGate struct {
	beginErr error
	begun    atomic.Int32
	finished atomic.Int32
	state    State
}

func (g *testGate) Begin(context.Context, Call, Spec, Input) (Ticket, error) {
	g.begun.Add(1)
	return Ticket{Key: "key"}, g.beginErr
}
func (g *testGate) Finish(_ context.Context, _ Ticket, r Result) error {
	g.state = r.State
	g.finished.Add(1)
	return nil
}

type testLeaser struct{ released chan struct{} }

func (l testLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() { close(l.released) }), nil
}
func testSpec() Spec {
	return Spec{Name: "tool", Conc: 1, Deadline: time.Minute, Placement: Core, Schema: func(raw json.RawMessage) (Input, error) {
		return Input{Arguments: raw, Target: "target", What: "Read", Where: "target", ScopeKey: "read"}, nil
	}}
}
func TestCancellationReturnsUnknownAndRetainsLeaseUntilAdapterExits(t *testing.T) {
	g := &testGate{}
	released := make(chan struct{})
	r := NewRegistry(g, testLeaser{released})
	started, stop := make(chan struct{}), make(chan struct{})
	if err := r.Register(testSpec(), func(context.Context, lease.Lease, Input) (Result, error) {
		close(started)
		<-stop
		return Result{State: Applied}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.Dispatch(ctx, Call{Key: "key", Name: "tool", Args: json.RawMessage(`{}`)})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if g.state != Unknown || g.finished.Load() != 1 {
		t.Fatal("missing unknown result")
	}
	select {
	case <-released:
		t.Fatal("released running adapter")
	default:
	}
	close(stop)
	<-released
	if g.state != Unknown {
		t.Fatal("late success replaced unknown")
	}
}
func TestPanicIsUnknown(t *testing.T) {
	g := &testGate{}
	r := NewRegistry(g, testLeaser{make(chan struct{})})
	if err := r.Register(testSpec(), func(context.Context, lease.Lease, Input) (Result, error) { panic("adapter") }); err != nil {
		t.Fatal(err)
	}
	result, err := r.Dispatch(t.Context(), Call{Key: "key", Name: "tool", Args: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrUnknown) || result.State != Unknown || g.finished.Load() != 1 {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestInvalidCallNeverReachesGate(t *testing.T) {
	g := &testGate{}
	r := NewRegistry(g, testLeaser{make(chan struct{})})
	if err := r.Register(testSpec(), func(context.Context, lease.Lease, Input) (Result, error) { t.Fatal("ran"); return Result{}, nil }); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", "broken"} {
		if _, err := r.Dispatch(t.Context(), Call{Key: "key", Name: "tool", Args: json.RawMessage(raw)}); err == nil {
			t.Fatal("invalid call accepted")
		}
	}
	if g.begun.Load() != 0 {
		t.Fatal("invalid call authorized")
	}
}

func TestUnresolvedClaimReturnsUnknownEffectState(t *testing.T) {
	for _, denial := range []error{ErrUnknown, ErrInProgress} {
		g := &testGate{beginErr: denial}
		r := NewRegistry(g, testLeaser{make(chan struct{})})
		if err := r.Register(testSpec(), func(context.Context, lease.Lease, Input) (Result, error) {
			t.Fatal("duplicate ran")
			return Result{}, nil
		}); err != nil {
			t.Fatal(err)
		}
		result, err := r.Dispatch(t.Context(), Call{Key: "key", Name: "tool", Args: json.RawMessage(`{}`)})
		if !errors.Is(err, denial) || result.State != Unknown {
			t.Fatalf("unresolved claim: %+v %v", result, err)
		}
	}
}

// Exercise the acceptance boundary directly: both the adapter outcome and
// cancellation are already observable. No select scheduling or sleeps decide
// whether this regression fails.
func TestOutcomeAfterCancellationIsUnknown(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			want := context.Canceled
			if deadline {
				ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
				want = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
			}
			defer cancel()
			<-ctx.Done()
			for _, state := range []State{Applied, Failed, Cancelled} {
				result, err := acceptOutcome(ctx, Input{Write: true}, Result{State: state, Revision: "r2", Body: json.RawMessage(`{"saved":true}`)}, nil)
				if result.State != Unknown || result.Revision != "" || len(result.Body) != 0 || !errors.Is(err, want) || !errors.Is(err, ErrUnknown) {
					t.Errorf("accepted late %s: %+v %v", state, result, err)
				}
			}
		})
	}
}
