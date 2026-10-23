// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/claudecode"
	"github.com/thisisjun786/lina/kit/lease"
)

type attempt interface {
	worker.Adapter
	Close(context.Context) error
}

type observation func() (json.RawMessage, []antigravity.DeniedAction)

// WorkerAvailable reports the adapter registrations used by explicit dispatch.
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
	return name == "antigravity" && s.cfg.Binary != "" || name == "claude" && s.cfg.ClaudeBinary != ""
}

// Keep vendor results intact in the artifact while sharing the durable dispatch
// and PR evidence path. Nothing is inferred from worker prose.
func (s *Service) attempt(name string) (attempt, observation, error) {
	switch name {
	case "antigravity":
		if s.cfg.Binary == "" {
			break
		}
		a := antigravity.New(antigravity.Config{Binary: s.cfg.Binary, Environment: s.cfg.Environment, Epoch: s.cfg.Epoch, Leaser: s.cfg.Activities})
		return a, observeAdapter(a), nil
	case "claude":
		if s.cfg.ClaudeBinary == "" {
			break
		}
		a := claudecode.New(claudecode.Config{Binary: s.cfg.ClaudeBinary, Environment: s.cfg.Environment, Epoch: s.cfg.Epoch, Leaser: s.cfg.Activities})
		return a, observeAdapter(a), nil
	}
	return nil, nil, errors.New("worker unavailable")
}

func observeAdapter(adapter worker.Adapter) observation {
	switch a := adapter.(type) {
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
	if name == "" {
		return s.cfg.Workers
	}
	for _, f := range s.cfg.Workers {
		if f.Name == name {
			return []Factory{f}
		}
	}
	return []Factory{{Name: name, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		a, _, err := s.attempt(name)
		if err != nil {
			return nil, nil, err
		}
		return a, a.Close, nil
	}}}
}
