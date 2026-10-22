// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type SettingFill struct {
	Quote string `json:"quote"`
	Name  string `json:"name"`
	Value string `json:"value"`
}
type SettingReceipt struct {
	Filled   bool   `json:"filled"`
	Revision string `json:"revision"`
}

// FillSetting stores the agent's blank value and its canonical evidence ID.
// It creates no unsolicited conversation notice and changes no voice revision.
func (p *Persona) FillSetting(ctx context.Context, scope PersonaScope, in SettingFill) (SettingReceipt, error) {
	if err := p.confirm(ctx, scope, in.Quote); err != nil {
		return SettingReceipt{}, err
	}
	state, err := p.agents.State(ctx, scope.Agent)
	if err != nil {
		return SettingReceipt{}, err
	}
	if agents.StateRevision(state) != scope.StateRevision {
		return SettingReceipt{}, agents.ErrConflict
	}
	filled, err := p.agents.FillBlank(ctx, scope.Agent, scope.StateRevision, in.Name, in.Value, scope.Input, scope.Person)
	if err != nil {
		return SettingReceipt{}, err
	}
	if filled {
		state.Settings[in.Name] = in.Value
		if state.SettingSources == nil {
			state.SettingSources = map[string]string{}
		}
		state.SettingSources[in.Name] = scope.Input
	}
	return SettingReceipt{Filled: filled, Revision: agents.StateRevision(state)}, nil
}

func RegisterPersonaSettings(registry *tools.Registry, p *Persona, scope PersonaScope) (tools.Spec, error) {
	if registry == nil || p == nil || scope.Agent == "" || scope.StateRevision == "" {
		return tools.Spec{}, tools.ErrInvalid
	}
	spec := tools.Spec{Classify: tools.Class(tools.PrivateDraft), Name: "fill_persona_setting", Placement: tools.Core, Conc: 1, Deadline: 10 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var in SettingFill
		if err := decodeSkill(raw, &in); err != nil || strings.TrimSpace(in.Quote) == "" || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Value) == "" || len(in.Value) > 8192 {
			return tools.Input{}, tools.ErrInvalid
		}
		args, err := json.Marshal(in)
		return tools.Input{Arguments: args, Target: "persona-state:" + scope.Agent, What: "Fill an agent setting", Where: "This agent", ScopeKey: "persona", Write: true}, err
	}}
	err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		var fill SettingFill
		err := decodeSkill(in.Arguments, &fill)
		var receipt SettingReceipt
		if err == nil {
			receipt, err = p.FillSetting(ctx, scope, fill)
		}
		if err != nil {
			return tools.Result{State: tools.Failed, Body: json.RawMessage(`{"code":"persona_setting_not_filled"}`)}, nil
		}
		body, err := json.Marshal(receipt)
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		return tools.Result{State: tools.Applied, Body: body, Revision: receipt.Revision}, nil
	})
	return spec, err
}

func PersonaSettingDeclaration() provider.ToolDecl {
	return json.RawMessage(`{"type":"function","name":"fill_persona_setting","description":"Fill a preset-defined empty setting about this agent from confirmed conversation. Quote the current person input supporting the value. Use only keys in persona_settings; never overwrite filled values. Facts about the person belong in memory, not these settings.","parameters":{"type":"object","properties":{"quote":{"type":"string"},"name":{"type":"string"},"value":{"type":"string"}},"required":["quote","name","value"],"additionalProperties":false}}`)
}
