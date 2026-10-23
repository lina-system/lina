// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestACPWorkerRegistration(t *testing.T) {
	dir := t.TempDir()
	for _, binary := range []string{"gemini", "cursor-agent", "grok", "claude-agent-acp", "claude"} {
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	s := &Service{cfg: Config{Workers: InstalledACPFactories(Config{})}}
	for _, name := range []string{"gemini", "cursor", "grok", "claude-acp"} {
		if !s.WorkerAvailable(name) {
			t.Errorf("installed ACP worker %s is not registered", name)
		}
	}
	if s.WorkerAvailable("unconfigured-agent") {
		t.Fatal("unconfigured worker is available")
	}
}

type lifecycleAdapter struct {
	quotaStopAdapter
	started bool
}

func (a *lifecycleAdapter) Name() string { return "lifecycle" }
func (a *lifecycleAdapter) Version(context.Context) (string, error) {
	if a.started {
		return "fixture-9", nil
	}
	return "unknown", nil
}
func (a *lifecycleAdapter) Start(ctx context.Context, l lease.Lease, s worker.StartSpec) (worker.Thread, error) {
	th, err := a.quotaStopAdapter.Start(ctx, l, s)
	<-a.events
	a.started = true
	a.events <- worker.Event{Thread: th, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "message", Text: "Writing result"}}
	return th, err
}
func TestDispatchInitializedVersionProgressAndInterruptedReceipt(t *testing.T) {
	s, r, _, _, _ := switchDispatchFixture(t, false)
	a := &lifecycleAdapter{quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 2)}}
	r.Definition.Worker = a.Name()
	if err := s.save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	s.cfg.Workers = []Factory{{Name: a.Name(), Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		return a, func(context.Context) error { return nil }, nil
	}}}
	seen := false
	s.cfg.Bind = func(_ Record, _ worker.Adapter, th worker.Thread, _, _ string) (func(worker.Event), func()) {
		return func(e worker.Event) {
			if e.Progress == nil {
				return
			}
			seen = true
			card, err := s.cfg.Work.Get(t.Context(), r.ID)
			if err != nil || !card.Active || card.Receipt != nil || len(card.Stages) == 0 || card.Stages[0].Summary != "Writing result" || card.Stages[0].Source != "worker_report" {
				t.Errorf("progress not projected while active: %+v %v", card, err)
			}
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 || records[0].Version != "fixture-9" {
				t.Errorf("initialized version not recorded: %+v %v", records, err)
			}
			a.events <- worker.Event{Thread: th, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Interrupted, Code: "cancelled"}}
		}, func() {}
	}
	s.execute(s.ctx, r)
	card, err := s.cfg.Work.Get(t.Context(), r.ID)
	if !seen || err != nil || card.Active || card.TurnStatus != "interrupted" || card.Receipt == nil || card.Receipt.Outcome != "interrupted" {
		t.Fatalf("interrupted finish became failure: seen=%v card=%+v err=%v", seen, card, err)
	}
}

func TestACPProfilesAreAcceptedByDispatchTool(t *testing.T) {
	d := definition(t)
	var declaration struct {
		Parameters struct {
			Properties map[string]struct{ Enum []string }
		}
	}
	if err := json.Unmarshal(Declaration(), &declaration); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-acp", "cursor", "gemini", "grok"} {
		d.Worker = name
		if err := validate(d); err != nil {
			t.Errorf("registered worker %s rejected: %v", name, err)
		}
		if !slices.Contains(declaration.Parameters.Properties["worker"].Enum, name) {
			t.Errorf("registered worker %s absent from tool schema", name)
		}
	}
}

func TestConfiguredWorkerFactoriesStayIsolatedFromHostDiscovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gemini"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	s, err := Open(t.Context(), Config{DB: store(t), Workers: []Factory{{Name: "configured", Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		t.Error("empty queue opened a worker")
		return nil, nil, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if len(s.cfg.Workers) != 1 || s.cfg.Workers[0].Name != "configured" || s.WorkerAvailable("gemini") {
		t.Fatalf("explicit factories widened to host installations: %+v", s.cfg.Workers)
	}
}
