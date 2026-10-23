// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/internal/core/worker"
	workeracp "github.com/thisisjun786/lina/internal/core/worker/acp"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/claudecode"
	"github.com/thisisjun786/lina/kit/lease"
)

type observation func() (json.RawMessage, []antigravity.DeniedAction)

// WorkerAvailable reports the adapter registrations available to dispatch.
// A dispatcher for another worker does not establish this worker's readiness.
func (s *Service) WorkerAvailable(name string) bool {
	if s == nil {
		return false
	}
	for _, f := range s.cfg.Workers {
		if f.Name == name && f.Open != nil {
			return true
		}
	}
	return false
}

func observeAdapter(adapter worker.Adapter) observation {
	switch a := adapter.(type) {
	case *workeracp.Adapter:
		return func() (json.RawMessage, []antigravity.DeniedAction) { return mustJSON(a.Result()), nil }
	case *antigravity.Adapter:
		return func() (json.RawMessage, []antigravity.DeniedAction) {
			result := a.Result()
			return mustJSON(result), result.DeniedActions
		}
	case *claudecode.Adapter:
		return func() (json.RawMessage, []antigravity.DeniedAction) {
			result := a.Result()
			var denied []antigravity.DeniedAction
			for _, denial := range result.PermissionDenials {
				denied = append(denied, antigravity.DeniedAction{Action: denial.ToolUseID, DisplayName: denial.ToolName})
			}
			return mustJSON(result), denied
		}
	}
	return func() (json.RawMessage, []antigravity.DeniedAction) { return nil, nil }
}

// Explicit selections retain their adapter choice; omitted selections use the
// installed chooser candidates. Both paths honor quota and attempt fencing.
func (s *Service) workersFor(r Record) []Factory {
	name := r.Definition.Worker
	if r.ReturnReceipt != "" {
		name = r.Worker
	}
	var factories []Factory
	for _, f := range s.cfg.Workers {
		if f.Open == nil {
			continue
		}
		if name == "" && f.Chooser {
			factories = append(factories, f)
		} else if name != "" && f.Name == name && (f.Explicit || r.ReturnReceipt != "") {
			return []Factory{f}
		}
	}
	return factories
}

// InstalledACPFactories makes ACP workers available to the same chooser as the
// existing adapters. Discovery only resolves the person's installed commands.
func InstalledACPFactories(cfg Config) []Factory {
	var factories []Factory
	for _, profile := range workeracp.Installed() {
		factories = append(factories, acpFactory(cfg, profile))
	}
	return factories
}

func acpFactory(cfg Config, profile workeracp.Profile) Factory {
	return Factory{Name: profile.Name, Explicit: true, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		a := workeracp.New(workeracp.Config{Profile: profile, Environment: cfg.Environment, Epoch: cfg.Epoch, Leaser: cfg.Activities})
		return a, a.Close, nil
	}}
}

// Registry metadata is independent of installation. Admission and rule files
// remain readable when a worker is absent; only configured factories can run.
func workerRegistry() []Factory {
	factories := []Factory{AntigravityFactory(Config{}), ClaudeFactory(Config{}), CodexFactory("")}
	for _, profile := range workeracp.BuiltinProfiles() {
		factories = append(factories, acpFactory(Config{}, profile))
	}
	return factories
}

func explicitWorkers(added ...Factory) []string {
	var names []string
	for _, f := range workerRegistry() {
		if f.Explicit {
			names = append(names, f.Name)
		}
	}
	for _, f := range added {
		if f.Explicit && f.Open != nil && !slices.Contains(names, f.Name) {
			names = append(names, f.Name)
		}
	}
	return names
}

func ruleWorker(name string) bool {
	for _, f := range workerRegistry() {
		if f.Name == name && f.Rules {
			return true
		}
	}
	return false
}

func registerBinaries(cfg Config) []Factory {
	factories := append([]Factory(nil), cfg.Workers...)
	add := func(f Factory) {
		for _, registered := range factories {
			if registered.Name == f.Name {
				return
			}
		}
		factories = append(factories, f)
	}
	if cfg.Binary != "" {
		f := AntigravityFactory(cfg)
		// An explicit factory list already defines the chooser and its order.
		f.Chooser = len(cfg.Workers) == 0
		add(f)
	}
	if cfg.ClaudeBinary != "" {
		add(ClaudeFactory(cfg))
	}
	return factories
}
