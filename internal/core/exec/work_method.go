// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
	"github.com/thisisjun786/lina/persona"
)

type WorkMethodFill struct {
	Quote string          `json:"quote"`
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (p *Persona) FillWorkMethod(ctx context.Context, scope PersonaScope, in WorkMethodFill) (SettingReceipt, error) {
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
	filled, err := p.agents.FillWorkMethod(ctx, scope.Agent, scope.Revision, scope.StateRevision, in.Key, in.Value, scope.Input, scope.Person)
	if err != nil {
		return SettingReceipt{}, err
	}
	if filled {
		// Canonicalization matches the store without reading a later state revision.
		var canonical bytes.Buffer
		_ = json.Compact(&canonical, in.Value)
		raw := canonical.Bytes()
		if state.WorkMethodFills == nil {
			state.WorkMethodFills = map[string]agents.WorkMethodFill{}
		}
		state.WorkMethodFills[in.Key] = agents.WorkMethodFill{Value: raw, Source: scope.Input}
	}
	return SettingReceipt{Filled: filled, Revision: agents.StateRevision(state)}, nil
}

func RegisterWorkMethod(registry *tools.Registry, p *Persona, scope PersonaScope) (tools.Spec, error) {
	if registry == nil || p == nil || scope.Agent == "" || scope.StateRevision == "" || scope.Revision <= 0 {
		return tools.Spec{}, tools.ErrInvalid
	}
	spec := tools.Spec{Classify: tools.Class(tools.PrivateDraft), Name: "fill_work_method", Placement: tools.Core, Conc: 1, Deadline: 10 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var in WorkMethodFill
		if decodeSkill(raw, &in) != nil || strings.TrimSpace(in.Quote) == "" || strings.TrimSpace(in.Key) == "" || !json.Valid(in.Value) || len(in.Value) > persona.MaxWorkMethodBytes {
			return tools.Input{}, tools.ErrInvalid
		}
		args, err := json.Marshal(in)
		return tools.Input{Arguments: args, Target: "persona-state:" + scope.Agent, What: "Fill a work-method setting", Where: "This agent", ScopeKey: "persona", Write: true}, err
	}}
	err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		var fill WorkMethodFill
		err := decodeSkill(in.Arguments, &fill)
		var receipt SettingReceipt
		if err == nil {
			receipt, err = p.FillWorkMethod(ctx, scope, fill)
		}
		if err != nil {
			return tools.Result{State: tools.Failed, Body: json.RawMessage(`{"code":"work_method_not_filled"}`)}, nil
		}
		body, err := json.Marshal(receipt)
		return tools.Result{State: tools.Applied, Body: body, Revision: receipt.Revision}, err
	})
	return spec, err
}

func WorkMethodDeclaration() provider.ToolDecl {
	return json.RawMessage(`{"type":"function","name":"fill_work_method","description":"Fill a declared work-method key for this agent. Use work_method.fill keys and a typed JSON value. Quote the current person input supporting it. Existing fills cannot be replaced.","parameters":{"type":"object","properties":{"quote":{"type":"string"},"key":{"type":"string"},"value":{}},"required":["quote","key","value"],"additionalProperties":false}}`)
}
