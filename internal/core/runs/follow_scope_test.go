// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestFollowSourceIsCheckedAfterDrainBeforeToolDispatch(t *testing.T) {
	t.Parallel()
	var revoked atomic.Bool
	var calls, effects atomic.Int32
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		calls.Add(1)
		revoked.Store(true) // The provider was in flight when the source was revoked.
		return call("read"), nil
	}, func(cfg *runs.Config) {
		cfg.AuthorizeFollowUp = func(_ context.Context, _ runs.Record, source json.RawMessage) error {
			if string(source) != `{"binding":"fixture"}` || revoked.Load() {
				return runs.ErrScope
			}
			return nil
		}
		tool := cfg.Tools["read"]
		tool.Handler.Execute = func(_ context.Context, c provider.ToolCall) loop.Result {
			effects.Add(1)
			return loop.Result{ID: c.ID, Code: loop.OK}
		}
		cfg.Tools["read"] = tool
	})
	id := start(t, s)
	if err := s.FollowScoped(t.Context(), id, "note", "Read appendix", json.RawMessage(`{"binding":"fixture"}`)); err != nil {
		t.Fatal(err)
	}
	release(t, s, id)
	record := wait(t, s, id)
	if effects.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("revoked source dispatched: models=%d tools=%d", calls.Load(), effects.Load())
	}
	if len(record.FollowUps) != 0 || len(record.FollowSources) != 1 {
		t.Fatalf("drain discarded source: pending=%d sources=%d", len(record.FollowUps), len(record.FollowSources))
	}
	if record.Outcome != runs.Failed {
		t.Fatalf("revoked scope accepted: %s", record.Outcome)
	}
}
func TestFollowSourceNeedsItsAuthorizer(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Fatal("unreleased run dispatched")
		return nil, nil
	})
	id := start(t, s)
	if err := s.FollowScoped(t.Context(), id, "note", "Read appendix", json.RawMessage(`{"binding":"fixture"}`)); err != runs.ErrScope {
		t.Fatalf("missing source authorizer: %v", err)
	}
	record, err := s.Get(t.Context(), id)
	if err != nil || len(record.FollowUps) != 0 {
		t.Fatalf("refused input persisted: %+v %v", record, err)
	}
}
