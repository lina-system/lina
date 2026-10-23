// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/claudecode"
)

type attempt interface {
	worker.Adapter
	Close(context.Context) error
}

type observation func() (json.RawMessage, []antigravity.DeniedAction)

// Keep vendor results intact in the artifact while sharing the durable dispatch
// and PR evidence path. Nothing is inferred from worker prose.
func (s *Service) attempt(name string) (attempt, observation, error) {
	switch name {
	case "antigravity":
		if s.cfg.Binary == "" {
			break
		}
		a := antigravity.New(antigravity.Config{Binary: s.cfg.Binary, Environment: s.cfg.Environment, Epoch: s.cfg.Epoch, Leaser: s.cfg.Activities})
		return a, func() (json.RawMessage, []antigravity.DeniedAction) {
			result := a.Result()
			return mustJSON(result), result.DeniedActions
		}, nil
	case "claude":
		if s.cfg.ClaudeBinary == "" {
			break
		}
		a := claudecode.New(claudecode.Config{Binary: s.cfg.ClaudeBinary, Environment: s.cfg.Environment, Epoch: s.cfg.Epoch, Leaser: s.cfg.Activities})
		return a, func() (json.RawMessage, []antigravity.DeniedAction) {
			result := a.Result()
			var denied []antigravity.DeniedAction
			for _, denial := range result.PermissionDenials {
				denied = append(denied, antigravity.DeniedAction{Action: denial.ToolUseID, DisplayName: denial.ToolName})
			}
			return mustJSON(result), denied
		}, nil
	}
	return nil, nil, errors.New("worker unavailable")
}
