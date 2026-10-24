// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func call(id string) provider.Stream {
	b, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": id, "name": "read", "arguments": "{}"})
	return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: b}, {Kind: provider.Terminal}}}
}
func wait(t *testing.T, s *runs.Service, id string) runs.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func release(t *testing.T, s *runs.Service, id string) {
	t.Helper()
	if err := s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}
func receive(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("event not observed")
	}
}

func TestNoAgeOrCallCapAndSteeringStaysInLaterRounds(t *testing.T) {
	t.Parallel()
	var s *runs.Service
	var id string
	var rounds atomic.Int64
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	s, _, _ = setupWith(t, func(ctx context.Context, req provider.Request) (provider.Stream, error) {
		n := rounds.Add(1)
		if _, ok := ctx.Deadline(); ok {
			t.Error("run deadline")
		}
		if n == 1 {
			if err := s.Follow(t.Context(), id, "check the appendix"); err != nil {
				t.Error(err)
			}
		} else {
			b, _ := json.Marshal(req.Items)
			if !strings.Contains(string(b), "check the appendix") {
				t.Errorf("round %d lost steering", n)
			}
		}
		now.Add(int64(24 * time.Hour))
		if n <= 129 {
			return call(fmt.Sprint(n)), nil
		}
		return answer(runs.Artifact{Body: "all evidence", Done: true}), nil
	}, func(c *runs.Config) { c.Now = func() time.Time { return time.Unix(0, now.Load()) } })
	b := brief()
	b.Checks = append(b.Checks, runs.Check{Name: "read", Kind: runs.ToolCalled})
	var err error
	id, _, err = s.Start(t.Context(), origin(), b)
	if err != nil {
		t.Fatal(err)
	}
	release(t, s, id)
	// 130 model rounds. The shared two-minute wait expires when the race
	// detector runs the rest of this package at the same time.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if err := s.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != runs.Complete || rounds.Load() != 130 || len(r.Calls) != 129 || len(r.Checks) != 2 || !r.Checks[1].Verified {
		t.Fatalf("rounds=%d record=%+v", rounds.Load(), r)
	}
}
func TestRetryResumesAfterToolRoundTrip(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	s, _, _ := setup(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
		n := calls.Add(1)
		if n == 1 {
			return call("read-once"), nil
		}
		if n == 2 {
			return nil, errors.New("disconnected")
		}
		b, _ := json.Marshal(req.Items)
		if !strings.Contains(string(b), "function_call_output") {
			t.Error("lost checkpoint")
		}
		return answer(runs.Artifact{Body: "continued report", Done: true}), nil
	})
	id := start(t, s)
	release(t, s, id)
	r := wait(t, s, id)
	if r.Outcome != runs.Complete || r.Retries != 1 || len(r.Calls) != 1 || calls.Load() != 3 {
		t.Fatalf("record=%+v calls=%d", r, calls.Load())
	}
}
func TestSecondFailureKeepsUsefulCheckpoint(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	s, _, store := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		if calls.Add(1) == 1 {
			return call("read-once"), nil
		}
		return nil, errors.New("disconnected")
	})
	id := start(t, s)
	release(t, s, id)
	r := wait(t, s, id)
	if r.Outcome != runs.Partial || r.Retries != 1 || calls.Load() != 3 {
		t.Fatalf("record=%+v", r)
	}
	b, err := store.Read(t.Context(), r.Material.ID, r.Material.Revision)
	if err != nil || !strings.Contains(string(b), "evidence") {
		t.Fatalf("gathered=%s error=%v", b, err)
	}
}
func TestCancellationWinsLateCompletion(t *testing.T) {
	t.Parallel()
	entered, finish := make(chan struct{}), make(chan struct{})
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		close(entered)
		<-finish
		return answer(runs.Artifact{Body: "late", Done: true}), nil
	})
	// Always unblock the injected uncooperative provider before service cleanup.
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	id := start(t, s)
	release(t, s, id)
	receive(t, entered)
	if err := s.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, s, id); r.Outcome != runs.Cancelled {
		t.Fatalf("record=%+v", r)
	}
	close(finish)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Get(t.Context(), id)
	if r.Outcome != runs.Cancelled {
		t.Fatalf("late completion changed cancellation: %+v", r)
	}
}
func TestConcurrencyReservationsWaitInOrder(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	})
	ids := make([]string, 10)
	for i := range ids {
		o := origin()
		if i >= 5 {
			o.Agent = "second-agent"
		}
		if i == 9 {
			o.Agent = "third-agent"
		}
		id, state, err := s.Start(t.Context(), o, brief())
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		want := runs.Working
		if i == 4 || i == 9 {
			want = runs.Waiting
		}
		if state != want {
			t.Fatalf("start %d state=%s want=%s", i, state, want)
		}
	}
	// Four first-agent slots hold its fifth run, independently of other agents.
	release(t, s, ids[4])
	r, _ := s.Get(t.Context(), ids[4])
	if r.State != runs.Waiting {
		t.Fatal("agent limit bypassed")
	}
	if err := s.Stop(t.Context(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, s, ids[4]); r.Outcome != runs.Complete {
		t.Fatalf("queued=%+v", r)
	}
}
func TestBudgetAndAccountHoldDoNotRetryOrLand(t *testing.T) {
	t.Parallel()
	called := make(chan struct{}, 2)
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		called <- struct{}{}
		return nil, &provider.Failure{Kind: provider.UsageCapped}
	})
	id := start(t, s)
	release(t, s, id)
	receive(t, called)
	r := wait(t, s, id)
	if r.State != runs.Waiting || r.Reason != "usage_capped" || r.Retries != 0 {
		t.Fatalf("quota=%+v", r)
	}
	select {
	case <-called:
		t.Fatal("quota retried automatically")
	default:
	}
	var count atomic.Int64
	s2, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		count.Add(1)
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	}, func(c *runs.Config) {
		c.Authorize = func(context.Context, runs.Origin, runs.Brief) error { return runs.ErrBudget }
	})
	if id, _, err := s2.Start(t.Context(), origin(), brief()); !errors.Is(err, runs.ErrBudget) || id != "" || count.Load() != 0 {
		t.Fatalf("budget start=%s calls=%d error=%v", id, count.Load(), err)
	}
}
func TestPerCallAuthorizationAndVerificationCannotBeClaimedByModel(t *testing.T) {
	t.Parallel()
	var executions atomic.Int64
	var calls atomic.Int64
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		if calls.Add(1) == 1 {
			return call("unauthorized"), nil
		}
		return answer(runs.Artifact{Body: "claimed done", Done: true}), nil
	}, func(c *runs.Config) {
		tool := c.Tools["read"]
		tool.Authorize = func(context.Context, runs.Record, provider.ToolCall) error { return runs.ErrScope }
		tool.Handler.Execute = func(context.Context, provider.ToolCall) loop.Result {
			executions.Add(1)
			return loop.Result{Code: loop.OK}
		}
		c.Tools["read"] = tool
	})
	b := brief()
	b.Checks = []runs.Check{{Name: "read", Kind: runs.ToolCalled}}
	id, _, err := s.Start(t.Context(), origin(), b)
	if err != nil {
		t.Fatal(err)
	}
	release(t, s, id)
	r := wait(t, s, id)
	if r.Outcome != runs.Partial || executions.Load() != 0 || r.Checks[0].Passed {
		t.Fatalf("record=%+v executions=%d", r, executions.Load())
	}
}

func TestInFlightRequestIsActivityAndCheckInIsNotDeadline(t *testing.T) {
	t.Parallel()
	entered, finish := make(chan struct{}), make(chan struct{})
	s, _, _ := setup(t, func(ctx context.Context, _ provider.Request) (provider.Stream, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-finish:
			return answer(runs.Artifact{Body: "long request", Done: true}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	id := start(t, s)
	release(t, s, id)
	receive(t, entered)
	r, _ := s.Get(t.Context(), id)
	if err := s.CheckProgress(t.Context(), time.Unix(0, r.Created).Add(48*time.Hour), 90*time.Second); err != nil {
		t.Fatal(err)
	}
	close(finish)
	r = wait(t, s, id)
	if r.Outcome != runs.Complete || r.Retries != 0 {
		t.Fatalf("in-flight request treated as stalled: %+v", r)
	}
}

func TestNeedsInputNoticeAndFollowUpReachOnlyThatRun(t *testing.T) {
	t.Parallel()
	var allowed atomic.Bool
	var calls atomic.Int64
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		n := calls.Add(1)
		if n <= 3 {
			return call(fmt.Sprint(n)), nil
		}
		return answer(runs.Artifact{Body: "answered", Done: true}), nil
	}, func(c *runs.Config) {
		tool := c.Tools["read"]
		tool.Authorize = func(context.Context, runs.Record, provider.ToolCall) error {
			if !allowed.Load() {
				return runs.ErrNeedsInput
			}
			return nil
		}
		c.Tools["read"] = tool
	})
	id := start(t, s)
	other := start(t, s)
	release(t, s, id)
	if r := wait(t, s, id); r.State != runs.NeedsInput || r.Retries != 0 {
		t.Fatalf("needs input=%+v", r)
	}
	notices, err := s.Notices(t.Context(), origin().Conversation)
	if err != nil || len(notices) != 1 || notices[0].ID != id {
		t.Fatalf("question notice=%+v error=%v", notices, err)
	}
	if err := s.Acknowledge(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Follow(t.Context(), id, "clarify the evidence request"); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, s, id); r.State != runs.NeedsInput {
		t.Fatalf("second question=%+v", r)
	}
	notices, err = s.Notices(t.Context(), origin().Conversation)
	if err != nil || len(notices) != 1 || notices[0].State != runs.NeedsInput {
		t.Fatalf("second question notice=%+v error=%v", notices, err)
	}
	if err := s.Acknowledge(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	allowed.Store(true)
	if err := s.Follow(t.Context(), id, "use the named evidence"); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, s, id); r.Outcome != runs.Complete {
		t.Fatalf("followed=%+v", r)
	}
	notices, err = s.Notices(t.Context(), origin().Conversation)
	if err != nil || len(notices) != 1 || notices[0].Outcome != runs.Complete {
		t.Fatalf("landing notice=%+v error=%v", notices, err)
	}
	untouched, _ := s.Get(t.Context(), other)
	if len(untouched.FollowUps) != 0 || untouched.Released {
		t.Fatal("steering reached another run")
	}
}
