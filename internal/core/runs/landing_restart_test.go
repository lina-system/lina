// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestPendingMaterialLandingResumesAfterRestart(t *testing.T) {
	for _, kind := range []string{"start", "promotion"} {
		t.Run(kind, func(t *testing.T) {
			var cfg runs.Config
			var calls atomic.Int32
			s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
				calls.Add(1)
				return answer(runs.Artifact{Body: "retained result", Done: true}), nil
			}, func(c *runs.Config) {
				flaky := &flakyMaterials{Store: c.Assets.(*assets.Store)}
				flaky.fail.Store(true)
				c.Assets = flaky
				cfg = *c
			})
			var id string
			if kind == "promotion" {
				handle, err := s.StartFromTurn(t.Context(), origin(), brief(), budget.Handoff{Turn: origin().Turn, Input: "inspect"})
				if err != nil {
					t.Fatal(err)
				}
				id = handle.ID
			} else {
				id = start(t, s)
			}
			release(t, s, id)
			if err := s.Wait(t.Context(), id); err == nil {
				t.Fatal("missing injected publication failure")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			restarted, err := runs.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if err = restarted.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			r, err := restarted.Get(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if r.State != runs.Landed && !r.Released {
				t.Fatalf("pending landing parked: state=%s pending=%s released=%v retries=%d", r.State, r.PendingOutcome, r.Released, r.Retries)
			}
			r = wait(t, restarted, id)
			if r.Outcome != runs.Complete || r.Material.ID == "" || r.Retries != 0 || calls.Load() != 1 {
				t.Fatalf("landing replayed execution: %+v calls=%d", r, calls.Load())
			}
		})
	}
}
