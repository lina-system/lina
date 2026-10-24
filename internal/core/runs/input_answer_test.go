// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/provider"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunInputWaitingAnswerIsAttributedAndResumesOnlyItsRun(t *testing.T) {
	t.Parallel()
	var allowed atomic.Bool
	s, db, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		if !allowed.Load() {
			return call("question"), nil
		}
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	}, func(cfg *runs.Config) {
		tool := cfg.Tools["read"]
		tool.Authorize = func(context.Context, runs.Record, provider.ToolCall) error {
			if !allowed.Load() {
				return runs.ErrNeedsInput
			}
			return nil
		}
		cfg.Tools["read"] = tool
	})
	// Extend this dependency's existing private fixture with the waiting schema.

	id := start(t, s)
	other := start(t, s)
	if err := s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	svc := waiting.New(db, map[string]waiting.Source{"run": &waiting.RunInputSource{DB: db, Owner: s}})
	snapshot, err := svc.Build(t.Context(), origin().Person, waiting.Options{Now: time.Now()})
	if err != nil || snapshot.Total != 1 || snapshot.Entries[0].Ref.ID != id || !snapshot.Entries[0].View.FreeText {
		t.Fatalf("input row: %+v %v", snapshot, err)
	}
	ref := snapshot.Entries[0].Ref
	if _, err := svc.Answer(t.Context(), "other-person", ref, "continue", time.Now()); !errors.Is(err, waiting.ErrPerson) {
		t.Fatalf("owner check: %v", err)
	}
	allowed.Store(true)
	d, err := svc.Answer(t.Context(), origin().Person, ref, "use the named source", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.Person != origin().Person || d.Agent != origin().Agent || d.Ref != ref {
		t.Fatalf("attribution: %+v", d)
	}
	if err := s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if again, err := svc.Answer(t.Context(), origin().Person, ref, "use the named source", time.Now()); err != nil || again != d {
		t.Fatalf("answer retry: %+v %v", again, err)
	}
	decisions, err := svc.Decisions(t.Context(), origin().Person)
	if err != nil || len(decisions) != 1 || decisions[0] != d {
		t.Fatalf("decisions: %+v %v", decisions, err)
	}
	r, err := s.Get(t.Context(), other)
	if err != nil || r.Released || len(r.InputAnswers) != 0 {
		t.Fatalf("another run changed: %+v %v", r, err)
	}
	snapshot, err = svc.Build(t.Context(), origin().Person, waiting.Options{Now: time.Now()})
	if err != nil || snapshot.Total != 0 {
		t.Fatalf("completed question remains: %+v %v", snapshot, err)
	}
}
