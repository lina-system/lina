// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

// RegisterSkillMaintenanceControl binds the main's existing job switch. A read
// has no enabled field; a write explicitly sets it. Undo is always available.
func RegisterSkillMaintenanceControl(registry *tools.Registry, control func(context.Context, *bool) (any, string, error)) (tools.Spec, error) {
	spec := tools.Spec{Name: "skill_maintenance", Placement: tools.Core, Conc: 1, Deadline: 10 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var p struct {
			Enabled *bool `json:"enabled,omitempty"`
		}
		if err := decodeSkill(raw, &p); err != nil {
			return tools.Input{}, err
		}
		normalized, _ := json.Marshal(p)
		return tools.Input{Arguments: normalized, Target: "skill-maintenance", What: "Read or change skill suggestions enablement", Where: "Skill catalog", ScopeKey: "skills", Write: p.Enabled != nil, Undoable: true}, nil
	}}
	err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		var p struct {
			Enabled *bool `json:"enabled,omitempty"`
		}
		if err := decodeSkill(in.Arguments, &p); err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		value, revision, err := control(ctx, p.Enabled)
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		raw, err := json.Marshal(value)
		return tools.Result{State: tools.Applied, Body: raw, Revision: revision}, err
	})
	return spec, err
}
