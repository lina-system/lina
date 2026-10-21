// SPDX-License-Identifier: Apache-2.0
package lane_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestSteeringAndStopTargetOnlyActiveTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		defer lanes.Close(context.Background())
		var q *loop.Inputs
		stopped := errors.New("typed stop")
		first, err := lanes.SubmitLoop(t.Context(), "conversation", func(ctx context.Context, inputs *loop.Inputs) error {
			q = inputs
			<-ctx.Done()
			return context.Cause(ctx)
		})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		item := provider.Item(`{"role":"user","content":"steer"}`)
		if !lanes.Steer("conversation", item) {
			t.Fatal("active turn rejected steering")
		}
		item[0] = ' '
		if got := q.Drain(); len(got) != 1 || string(got[0]) != `{"role":"user","content":"steer"}` {
			t.Fatalf("steering snapshot: %s", got)
		}
		if lanes.Steer("other", item) || lanes.Stop("other", stopped) {
			t.Fatal("controlled another conversation")
		}
		next := submit(t, lanes, t.Context(), "conversation", func(context.Context) error { return nil })
		if !lanes.Stop("conversation", stopped) {
			t.Fatal("active stop rejected")
		}
		if lanes.Steer("conversation", item) {
			t.Fatal("stopped turn accepted input")
		}
		if err := <-first; !errors.Is(err, stopped) {
			t.Fatal(err)
		}
		if err := <-next; err != nil {
			t.Fatal(err)
		}
		if lanes.Stop("conversation", stopped) || lanes.Steer("conversation", item) {
			t.Fatal("idle conversation accepted control")
		}
	})
}

func TestClosedSteeringQueueReturnsOwnershipToCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		defer lanes.Close(context.Background())
		finish := make(chan struct{})
		result, err := lanes.SubmitLoop(t.Context(), "conversation", func(_ context.Context, inputs *loop.Inputs) error {
			inputs.Close()
			<-finish
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if lanes.Steer("conversation", provider.Item(`{}`)) {
			t.Fatal("closed queue swallowed follow-up")
		}
		close(finish)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		var pending []provider.Item
		result, err = lanes.SubmitLoop(t.Context(), "conversation", func(_ context.Context, inputs *loop.Inputs) error {
			pending = inputs.Drain()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pending, []provider.Item(nil)) {
			t.Fatalf("reused old queue: %s", pending)
		}
	})
}

func TestSteeringOrdinaryTurnIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lanes := lane.New()
		defer lanes.Close(context.Background())
		release := make(chan struct{})
		result := submit(t, lanes, t.Context(), "conversation", func(context.Context) error { <-release; return nil })
		synctest.Wait()
		accepted := lanes.Steer("conversation", provider.Item(`{"role":"user","content":"follow-up"}`))
		close(release)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if accepted {
			t.Fatal("ordinary turn consumed steering instead of retaining caller ownership")
		}
	})
}
