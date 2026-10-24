// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
	"sync/atomic"
	"testing"
	"time"
)

func TestNoticeTransferAcknowledgesOnlyDurableEnqueue(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	})
	id := start(t, s)
	if err := s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("outbox unavailable")
	var attempted runs.Notice
	if err := s.TransferNotices(t.Context(), origin().Conversation, func(_ context.Context, n runs.Notice) error { attempted = n; return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if attempted.Key == "" || attempted.Landed.IsZero() {
		t.Fatalf("missing durable generation: %+v", attempted)
	}
	pending, err := s.Notices(t.Context(), origin().Conversation)
	if err != nil || len(pending) != 1 || pending[0].Key != attempted.Key {
		t.Fatalf("transfer lost notice: %+v %v", pending, err)
	}
	count := 0
	for range 2 {
		if err := s.TransferNotices(t.Context(), origin().Conversation, func(_ context.Context, n runs.Notice) error {
			count++
			if n.Key != attempted.Key || !n.Landed.Equal(attempted.Landed) {
				t.Fatal("retry changed landing")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("duplicate transfer: %d", count)
	}
}
func TestNoticeGenerationChangesAtSameClockInstant(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var allowed atomic.Bool
	var calls atomic.Int64
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		if calls.Add(1) == 1 {
			return call("question"), nil
		}
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	}, func(cfg *runs.Config) {
		cfg.Now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
		tool := cfg.Tools["read"]
		tool.Authorize = func(context.Context, runs.Record, provider.ToolCall) error {
			if !allowed.Load() {
				return runs.ErrNeedsInput
			}
			return nil
		}
		cfg.Tools["read"] = tool
	})
	id := start(t, s)
	if err := s.Release(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
	first, err := s.Notices(ctx, origin().Conversation)
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%v %v", first, err)
	}
	allowed.Store(true)
	if err := s.Follow(ctx, id, "continue"); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
	second, err := s.Notices(ctx, origin().Conversation)
	if err != nil || len(second) != 1 || second[0].Key == first[0].Key || !second[0].Landed.Equal(first[0].Landed) {
		t.Fatalf("generations=%+v %+v %v", first, second, err)
	}
}
