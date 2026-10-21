// SPDX-License-Identifier: Apache-2.0
package lane_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/internal/core/lane"
)

func submit(t *testing.T, lanes *lane.Manager, ctx context.Context, conversation string, turn lane.Turn) <-chan error {
	t.Helper()
	result, err := lanes.Submit(ctx, conversation, turn)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestConversationSerializesTurnsAndOtherConversationsProceed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		defer lanes.Close(context.Background())
		release := make(chan struct{})
		var order []int
		first := submit(t, lanes, t.Context(), "a", func(context.Context) error {
			order = append(order, 1)
			<-release
			return nil
		})
		synctest.Wait()
		second := submit(t, lanes, t.Context(), "a", func(context.Context) error { order = append(order, 2); return nil })
		third := submit(t, lanes, t.Context(), "a", func(context.Context) error { order = append(order, 3); return nil })
		other := submit(t, lanes, t.Context(), "b", func(context.Context) error { return nil })
		if err := <-other; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !reflect.DeepEqual(order, []int{1}) {
			t.Fatalf("overlapping turns: %v", order)
		}
		close(release)
		for _, result := range []<-chan error{first, second, third} {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if _, ok := <-result; ok {
				t.Fatal("more than one result")
			}
		}
		if !reflect.DeepEqual(order, []int{1, 2, 3}) {
			t.Fatalf("turn order: %v", order)
		}
	})
}

func TestCanceledQueuedTurnDoesNotRunAndFailureDoesNotBlockNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		defer lanes.Close(context.Background())
		release := make(chan struct{})
		failure := errors.New("turn failed")
		first := submit(t, lanes, t.Context(), "a", func(context.Context) error { <-release; return failure })
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		canceled := submit(t, lanes, ctx, "a", func(context.Context) error { t.Error("canceled turn ran"); return nil })
		cancel()
		next := submit(t, lanes, t.Context(), "a", func(context.Context) error { return nil })
		close(release)
		if err := <-first; !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if err := <-canceled; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := <-next; err != nil {
			t.Fatal(err)
		}
	})
}

func TestCancellationReachesRunningTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		defer lanes.Close(context.Background())
		ctx, cancel := context.WithCancelCause(t.Context())
		cause := errors.New("person stopped turn")
		result := submit(t, lanes, ctx, "a", func(ctx context.Context) error { <-ctx.Done(); return context.Cause(ctx) })
		synctest.Wait()
		cancel(cause)
		if err := <-result; !errors.Is(err, cause) {
			t.Fatal(err)
		}
	})
}

func TestCloseCancelsActiveAndQueuedTurnsAndWaitsForExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		release := make(chan struct{})
		active := submit(t, lanes, t.Context(), "a", func(ctx context.Context) error {
			<-ctx.Done()
			<-release
			return context.Cause(ctx)
		})
		synctest.Wait()
		queued := submit(t, lanes, t.Context(), "a", func(context.Context) error { t.Error("queued turn started after close"); return nil })
		closed := make(chan error, 1)
		go func() { closed <- lanes.Close(t.Context()) }()
		synctest.Wait()
		select {
		case err := <-closed:
			t.Fatalf("close returned with active turn: %v", err)
		default:
		}
		if _, err := lanes.Submit(t.Context(), "b", func(context.Context) error { return nil }); !errors.Is(err, lane.ErrClosed) {
			t.Fatalf("submit after close: %v", err)
		}
		close(release)
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		for _, result := range []<-chan error{active, queued} {
			if err := <-result; !errors.Is(err, lane.ErrClosed) {
				t.Fatalf("shutdown result: %v", err)
			}
		}
		if err := lanes.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCloseDeadlineDoesNotAllowReplacementTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		release := make(chan struct{})
		result := submit(t, lanes, t.Context(), "a", func(context.Context) error { <-release; return nil })
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := lanes.Close(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := lanes.Submit(t.Context(), "a", func(context.Context) error { return nil }); !errors.Is(err, lane.ErrClosed) {
			t.Fatal(err)
		}
		close(release)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if err := lanes.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInvalidAdmissionAndIdleConversationReuse(t *testing.T) {
	lanes := lane.New()
	defer lanes.Close(context.Background())
	turn := func(context.Context) error { return nil }
	if _, err := lanes.Submit(t.Context(), "", turn); !errors.Is(err, lane.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := lanes.Submit(t.Context(), "a", nil); !errors.Is(err, lane.ErrInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := lanes.Submit(ctx, "a", turn); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for range 20 {
		if err := <-submit(t, lanes, t.Context(), "a", turn); err != nil {
			t.Fatal(err)
		}
	}
}
