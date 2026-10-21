// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func preparation() *turn.Preparation {
	return &turn.Preparation{
		VerifyBuild: func(context.Context) error { return nil },
		Account: func(context.Context) (provider.Credential, error) {
			return provider.NewCredential("account", provider.LoopbackOAuth, "synthetic"), nil
		},
		Resolve: func(context.Context) (turn.ModelPolicy, error) {
			return turn.ModelPolicy{Model: "model", Effort: "medium", Revision: "policy-1", Class: provider.ModelAPI}, nil
		},
		Context: func(context.Context, turn.ModelPolicy) (turn.PreparedRequest, error) {
			return turn.PreparedRequest{Request: provider.Request{Items: []provider.Item{json.RawMessage(`{"role":"user","content":"input"}`)}}, Assembly: turn.Assembly{AgentID: "agent", PersonaRevision: "persona-1", Items: []turn.Reference{{ID: "context-1"}}}}, nil
		},
		Acknowledge:   func(context.Context, turn.Acknowledgement) error { return nil },
		RecordRequest: func(context.Context, turn.RequestEvidence) error { return nil },
		Digest:        func(provider.Request) string { return strings.Repeat("a", 64) },
	}
}

func TestPreparationStopsAtEachFailedStep(t *testing.T) {
	for _, failed := range []turn.Step{turn.BuildStep, turn.AccountStep, turn.ModelStep, turn.ContextStep} {
		t.Run(string(failed), func(t *testing.T) {
			db, _ := database(t)
			s := turn.New(db)
			p := preparation()
			var order []turn.Step
			var shown []turn.PreparationFailure
			failure := errors.New("private details\nnever display")
			check := func(step turn.Step) error {
				order = append(order, step)
				if failed == step {
					return failure
				}
				return nil
			}
			p.VerifyBuild = func(context.Context) error { return check(turn.BuildStep) }
			account := p.Account
			p.Account = func(ctx context.Context) (provider.Credential, error) {
				c, _ := account(ctx)
				return c, check(turn.AccountStep)
			}
			resolve := p.Resolve
			p.Resolve = func(ctx context.Context) (turn.ModelPolicy, error) {
				m, _ := resolve(ctx)
				return m, check(turn.ModelStep)
			}
			build := p.Context
			p.Context = func(ctx context.Context, m turn.ModelPolicy) (turn.PreparedRequest, error) {
				r, _ := build(ctx, m)
				return r, check(turn.ContextStep)
			}
			acks, requests := 0, 0
			p.Acknowledge = func(context.Context, turn.Acknowledgement) error { acks++; return nil }
			p.Show = func(f turn.PreparationFailure) { shown = append(shown, f) }
			_, err := s.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Preparation: p, Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) { requests++; return &stream{}, nil }}})
			if !errors.Is(err, failure) || requests != 0 || acks != 0 || len(shown) != 1 || shown[0].Step != failed || shown[0].Line == "" || strings.ContainsAny(shown[0].Line, "\r\n") || strings.Contains(shown[0].Line, "private") {
				t.Fatalf("err=%v requests=%d acks=%d shown=%+v", err, requests, acks, shown)
			}
			steps := []turn.Step{turn.BuildStep, turn.AccountStep, turn.ModelStep, turn.ContextStep}
			if !reflect.DeepEqual(order, steps[:len(order)]) || order[len(order)-1] != failed {
				t.Fatal(order)
			}
			row, _ := s.Read(t.Context(), "turn")
			if row.Prepared || row.State != turn.Failed {
				t.Fatal(row)
			}
		})
	}
}

func TestPreparationCommitFailureAndSignIn(t *testing.T) {
	for _, stage := range []string{"sign-in", "ack", "request"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := database(t)
			s := turn.New(db)
			p := preparation()
			var shown []turn.PreparationFailure
			p.Show = func(f turn.PreparationFailure) { shown = append(shown, f) }
			fail := errors.New("commit failed")
			switch stage {
			case "sign-in":
				p.Account = func(context.Context) (provider.Credential, error) {
					return provider.Credential{}, &provider.Failure{Kind: provider.NeedsSignIn}
				}
			case "ack":
				p.Acknowledge = func(context.Context, turn.Acknowledgement) error { return fail }
			case "request":
				p.RecordRequest = func(context.Context, turn.RequestEvidence) error { return fail }
			}
			calls := 0
			_, err := s.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Preparation: p, Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) { calls++; return &stream{}, nil }}})
			if err == nil || calls != 0 || len(shown) != 1 {
				t.Fatalf("%v calls=%d shown=%+v", err, calls, shown)
			}
			if stage == "sign-in" && !shown[0].SignIn {
				t.Fatal("sign-in offer absent")
			}
		})
	}
}

func TestPreparedRequestRetainedAcrossFollowUp(t *testing.T) {
	db, _ := database(t)
	s := turn.New(db)
	p := preparation()
	builds, acks := 0, 0
	var evidence []turn.RequestEvidence
	original := p.Context
	p.Context = func(ctx context.Context, m turn.ModelPolicy) (turn.PreparedRequest, error) {
		builds++
		return original(ctx, m)
	}
	p.Acknowledge = func(_ context.Context, a turn.Acknowledgement) error {
		acks++
		if a.PolicyRevision != "policy-1" || a.Assembly.AgentID != "agent" {
			t.Error(a)
		}
		return nil
	}
	p.RecordRequest = func(_ context.Context, e turn.RequestEvidence) error { evidence = append(evidence, e); return nil }
	inputs := &loop.Inputs{}
	var requests []provider.Request
	cfg := loop.Config{Inputs: inputs, Stream: func(_ context.Context, r provider.Request) (provider.Stream, error) {
		if acks != 1 || len(evidence) != len(requests)+1 {
			t.Fatal("request before evidence commit")
		}
		requests = append(requests, r)
		if len(requests) == 1 {
			inputs.Push(json.RawMessage(`{"role":"user","content":"steering"}`))
		}
		return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
	}}
	_, err := s.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Preparation: p, Loop: cfg})
	if err != nil || builds != 1 || acks != 1 || len(requests) != 2 {
		t.Fatalf("%v builds=%d acks=%d requests=%d", err, builds, acks, len(requests))
	}
	if !reflect.DeepEqual(requests[0].Items, requests[1].Items[:len(requests[0].Items)]) || requests[0].Model != "model" || requests[0].Credential.Account != "account" {
		t.Fatal("preparation not applied or prefix changed")
	}
	for i, e := range evidence {
		if e.Round != i || e.Class != provider.ModelAPI || e.Hash == "" {
			t.Fatal(e)
		}
	}
	row, _ := s.Read(t.Context(), "turn")
	if len(row.Items) != 0 {
		t.Fatal("input packet persisted as history")
	}
}
