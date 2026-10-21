// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestPreparationFailureHandsBackSteering(t *testing.T) {
	db, _ := database(t)
	inputs := &loop.Inputs{}
	item := provider.Item(`{"role":"user","content":"keep this input"}`)
	inputs.Push(item)
	failure := errors.New("preparation failed")
	out, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Prepare: func(context.Context) error { return failure }, Loop: loop.Config{Inputs: inputs}})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(out.FollowUps) != 1 || string(out.FollowUps[0]) != string(item) {
		t.Fatalf("lost steering on preparation failure: %+v", out)
	}
	if inputs.Push(item) {
		t.Fatal("preparation failure left steering admission open")
	}
}

func TestLaneSteeringReachesNextRoundOnly(t *testing.T) {
	db, _ := database(t)
	store := turn.New(db)
	lanes := lane.New()
	defer lanes.Close(context.Background())
	received := make(chan provider.Request, 2)
	release := make(chan struct{})
	original := provider.Item(`{"role":"user","content":"original"}`)
	steering := provider.Item(`{"role":"user","content":"new direction"}`)
	rounds := 0
	initialID := admit(t, db)
	result, err := lanes.SubmitLoop(t.Context(), "conversation", func(ctx context.Context, inputs *loop.Inputs) error {
		_, err := store.Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: initialID, Loop: loop.Config{Inputs: inputs, Request: provider.Request{Items: []provider.Item{original}}, Stream: func(_ context.Context, req provider.Request) (provider.Stream, error) {
			rounds++
			received <- req
			if rounds == 1 {
				<-release
			}
			return reviewResponse(), nil
		}}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	first := <-received
	if len(first.Items) != 1 || string(first.Items[0]) != string(original) {
		t.Fatalf("first request: %s", first.Items)
	}
	// Admission commits before the lane sees this steering message.
	_, err = inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: "steering"}, Body: steering})
	if err != nil {
		t.Fatal(err)
	}
	if !lanes.Steer("conversation", steering) {
		t.Fatal("steering rejected")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	second := <-received
	if len(second.Items) != 2 || string(second.Items[0]) != string(original) || string(second.Items[1]) != string(steering) || rounds != 2 {
		t.Fatalf("next request: %s rounds=%d", second.Items, rounds)
	}
	if lanes.Steer("conversation", steering) {
		t.Fatal("completed turn consumed follow-up")
	}
}

func TestStopAbortsOpenCallsWithTypedCause(t *testing.T) {
	for _, issuer := range []turn.Issuer{turn.Person, turn.Watchdog, turn.EpochLost, turn.GrantRevoked, turn.Budget, turn.Shutdown} {
		t.Run(string(issuer), func(t *testing.T) {
			db, _ := database(t)
			store := turn.New(db)
			lanes := lane.New()
			defer lanes.Close(context.Background())
			started := make(chan struct{})
			initialID := admit(t, db)
			eventID := inputEvent(t, db)
			var out loop.Outcome
			result, err := lanes.SubmitLoop(t.Context(), "conversation", func(ctx context.Context, inputs *loop.Inputs) error {
				var err error
				out, err = store.Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: initialID, InputEvent: eventID, Loop: loop.Config{Inputs: inputs, Request: provider.Request{Tools: []provider.ToolDecl{provider.Item(`{"type":"function","name":"read"}`)}}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
					return reviewResponse(provider.ToolCall{ID: "a", Name: "read", Arguments: `{}`}, provider.ToolCall{ID: "b", Name: "read", Arguments: `{}`}), nil
				}, Tools: map[string]loop.Tool{"read": {
					Validate: func(provider.ToolCall) (string, error) { return "same-target", nil },
					Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
						if c.ID != "a" {
							t.Error("queued call ran after stop")
						}
						close(started)
						<-ctx.Done()
						return loop.Result{ID: c.ID, Code: loop.Aborted}
					},
				}}}})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			cause := turn.Interrupt{Issuer: issuer}
			if !lanes.Stop("conversation", cause) {
				t.Fatal("stop rejected")
			}
			var interrupted turn.Interrupt
			if err := <-result; !errors.As(err, &interrupted) || interrupted.Issuer != issuer {
				t.Fatalf("lost issuer: %v", err)
			}
			row, err := store.Read(t.Context(), "turn")
			if err != nil || row.State != turn.Aborted || len(out.Results) != 2 || len(row.Results) != 2 {
				t.Fatalf("out=%+v row=%+v err=%v", out, row, err)
			}
			for _, r := range row.Results {
				if r.Code != loop.Aborted {
					t.Fatal(r)
				}
			}
		})
	}
}

func TestFourStepPreparationFailureHandsBackSteering(t *testing.T) {
	db, _ := database(t)
	inputs := &loop.Inputs{}
	item := provider.Item(`{"role":"user","content":"pending direction"}`)
	inputs.Push(item)
	p := preparation()
	failure := errors.New("account unavailable")
	p.Account = func(context.Context) (provider.Credential, error) { return provider.Credential{}, failure }
	requests := 0
	out, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Preparation: p, Loop: loop.Config{Inputs: inputs, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		return reviewResponse(), nil
	}}})
	if !errors.Is(err, failure) || requests != 0 || len(out.FollowUps) != 1 || string(out.FollowUps[0]) != string(item) || inputs.Push(item) {
		t.Fatalf("err=%v requests=%d handoff=%s", err, requests, out.FollowUps)
	}
}
