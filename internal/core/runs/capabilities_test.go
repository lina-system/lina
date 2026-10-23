// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestRunCacheIdentityAndEvidenceSurviveRestart(t *testing.T) {
	checkCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var cfg runs.Config
	sent := make(chan provider.Request, 1)
	s, _, _ := setupWith(t, func(ctx context.Context, r provider.Request) (provider.Stream, error) {
		sent <- r
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(c *runs.Config) {
		c.Model = "model"
		c.ProviderClass = provider.ModelAPI
		c.SelectCapabilities = func(r provider.Request) (provider.Request, error) {
			var err error
			caps := provider.CapabilitySet{provider.CacheIdentityCapability}
			r.Capabilities, err = assemble.SelectCapabilities(r, caps, caps)
			return r, err
		}
		cfg = *c
	})
	id := start(t, s)
	if err := s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	var first provider.Request
	select {
	case first = <-sent:
	case <-checkCtx.Done():
		t.Fatal(checkCtx.Err())
	}
	record, err := s.Get(t.Context(), id)
	if err != nil || len(record.Requests) != 1 || record.Requests[0].Hash != assemble.Digest(first) || record.Requests[0].CacheIdentity != assemble.RunIdentity(id) {
		t.Fatalf("missing pre-send evidence: %+v %v", record.Requests, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	cfg.Stream = func(_ context.Context, r provider.Request) (provider.Stream, error) {
		sent <- r
		return answer(runs.Artifact{Body: "result", Done: true}), nil
	}
	reopened, err := runs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Recovery retains an interrupted run until Core explicitly releases it.
	if err := reopened.Release(checkCtx, id); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Wait(checkCtx, id); err != nil {
		t.Fatal(err)
	}
	var second provider.Request
	select {
	case second = <-sent:
	case <-checkCtx.Done():
		t.Fatal(checkCtx.Err())
	}
	record, err = reopened.Get(t.Context(), id)
	if err != nil || len(record.Requests) != 2 || record.Requests[1].Hash != assemble.Digest(second) || first.CacheIdentity != second.CacheIdentity || second.Capabilities.CacheIdentity != first.CacheIdentity {
		t.Fatalf("restart lost metadata: %+v %v", record.Requests, err)
	}
	if assemble.RunIdentity(id) == assemble.RunIdentity(id+"other") || assemble.RunIdentity(id) == assemble.ConversationIdentity("", "", id) {
		t.Fatal("identity domains collide")
	}
}
