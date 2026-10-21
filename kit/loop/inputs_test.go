// SPDX-License-Identifier: Apache-2.0
package loop_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Intercept the final empty snapshot while retaining the real admission queue.
// Channels place the producer between the last drain and Run's return.
type boundaryInputs struct {
	*loop.Inputs
	drains          int
	afterFinalDrain func()
}

func (q *boundaryInputs) Drain() []provider.Item {
	items := q.Inputs.Drain()
	q.drains++
	if q.drains == 2 {
		q.afterFinalDrain()
	}
	return items
}
func TestSteeringAfterFinalDrainReturnsAsFollowUp(t *testing.T) {
	inputs := &boundaryInputs{Inputs: &loop.Inputs{}}
	late := json.RawMessage(`{"role":"user","content":"next turn"}`)
	drained, pushed := make(chan struct{}), make(chan bool, 1)
	inputs.afterFinalDrain = func() {
		close(drained)
		if !<-pushed {
			t.Error("input before handoff was rejected")
		}
	}
	go func() { <-drained; pushed <- inputs.Push(late) }()
	requests := 0
	out, err := loop.Run(t.Context(), loop.Config{Inputs: inputs, Stream: func(context.Context, provider.Request) (provider.Stream, error) { requests++; return response(), nil }})
	if err != nil || requests != 1 || len(out.FollowUps) != 1 || string(out.FollowUps[0]) != string(late) {
		t.Fatalf("out=%+v requests=%d err=%v", out, requests, err)
	}
	if len(inputs.Drain()) != 0 {
		t.Error("returned follow-up remains in queue")
	}
	if inputs.Push(json.RawMessage(`{"role":"user","content":"after close"}`)) {
		t.Error("closed steering accepted input instead of returning ownership to the lane")
	}
}
func TestQueuedSteeringReturnsAsFollowUpOnErrors(t *testing.T) {
	failure := errors.New("request failed")
	for _, stage := range []string{"stream", "transform", "canceled", "configuration"} {
		t.Run(stage, func(t *testing.T) {
			inputs := &loop.Inputs{}
			late := json.RawMessage(`{"role":"user","content":"retain on error"}`)
			cfg := loop.Config{Inputs: inputs, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
				inputs.Push(late)
				return nil, failure
			}}
			ctx := t.Context()
			want := failure
			switch stage {
			case "stream":
			case "transform":
				cfg.Transform = func(_ context.Context, r provider.Request) (provider.Request, error) {
					inputs.Push(late)
					return r, failure
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				inputs.Push(late)
				want = context.Canceled
			case "configuration":
				cfg.Stream = nil
				inputs.Push(late)
				want = loop.ErrConfig
			}
			out, err := loop.Run(ctx, cfg)
			if !errors.Is(err, want) || len(out.FollowUps) != 1 || string(out.FollowUps[0]) != string(late) {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if len(inputs.Drain()) != 0 || inputs.Push(late) {
				t.Error("error exit left steering admission open or retained handed-off input")
			}
		})
	}
}
func TestInputCloseAndPushHaveExactlyOneOwner(t *testing.T) {
	inputs := &loop.Inputs{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	type admission struct {
		item     string
		accepted bool
	}
	admissions := make(chan admission, 64)
	for i := range 64 {
		wg.Go(func() {
			<-start
			item := fmt.Sprintf(`{"content":"%d"}`, i)
			admissions <- admission{item, inputs.Push(json.RawMessage(item))}
		})
	}
	finished := make(chan []provider.Item, 1)
	wg.Go(func() { <-start; finished <- inputs.Close() })
	close(start)
	wg.Wait()
	close(admissions)
	handed := map[string]bool{}
	for _, item := range <-finished {
		if handed[string(item)] {
			t.Fatalf("duplicate input: %s", item)
		}
		handed[string(item)] = true
	}
	for a := range admissions {
		if handed[a.item] != a.accepted {
			t.Fatalf("input %s: accepted=%v handed=%v", a.item, a.accepted, handed[a.item])
		}
	}
	if len(inputs.Drain()) != 0 || len(inputs.Close()) != 0 || inputs.Push(json.RawMessage(`{}`)) {
		t.Fatal("closed queue retained or admitted input")
	}
}
