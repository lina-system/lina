// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type NameChange struct {
	Subject        string   `json:"subject"`
	Current        string   `json:"current"`
	Quote          string   `json:"quote"`
	RetiredAliases []string `json:"retired_aliases,omitempty"`
}

// RememberName uses the confirmed current person event, never a model-supplied
// holder, scope or evidence id. The ordinary memory writer owns corrections.
func (p *Persona) RememberName(ctx context.Context, scope PersonaScope, in NameChange) (RememberReceipt, error) {
	event, err := p.confirmed(ctx, scope, in.Quote)
	if err != nil {
		return RememberReceipt{}, err
	}
	if strings.TrimSpace(in.Subject) == "" || len(in.Subject) > 256 || !strings.Contains(in.Quote, in.Current) {
		return RememberReceipt{}, memory.ErrInvalid
	}
	for _, alias := range in.RetiredAliases {
		if !strings.Contains(in.Quote, alias) {
			return RememberReceipt{}, memory.ErrInvalid
		}
	}
	request := memory.Write{Agent: scope.Agent, Person: scope.Person, Item: memory.Item{
		Kind: memory.Name, Content: in.Quote, Name: &memory.NameValue{Current: in.Current, RetiredAliases: in.RetiredAliases}, Subject: memory.Subject{Kind: memory.World, ID: in.Subject}, Basis: memory.Stated,
		Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}},
	}}
	names, err := p.memory.store.Names(ctx, memory.RecallScope{Agent: scope.Agent, Person: scope.Person, ExternalModel: true}, p.now().Unix())
	if err != nil {
		return RememberReceipt{}, err
	}
	identified := strings.Contains(in.Quote, in.Subject)
	matches := 0
	for _, hit := range names {
		if hit.Subject != request.Item.Subject {
			continue
		}
		matches++
		if hit.Name != nil && strings.Contains(in.Quote, hit.Name.Current) {
			identified = true
		}
		request.Item.Holder, request.Item.Scope = hit.Holder, hit.Scope
		request.ExplicitHolder = true
	}
	if !identified || matches > 1 {
		return RememberReceipt{}, memory.ErrInvalid
	}
	return p.memory.Remember(ctx, request)
}

func RegisterName(registry *tools.Registry, p *Persona, scope PersonaScope) (tools.Spec, error) {
	spec := tools.Spec{Classify: tools.Class(tools.PrivateDraft), Name: "remember_name", Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var in NameChange
		if err := decodeSkill(raw, &in); err != nil || strings.TrimSpace(in.Subject) == "" || len(in.Subject) > 256 || strings.TrimSpace(in.Current) == "" || len(in.Current) > 256 || strings.TrimSpace(in.Quote) == "" || len(in.Quote) > 8192 {
			return tools.Input{}, tools.ErrInvalid
		}
		args, err := json.Marshal(in)
		return tools.Input{Arguments: args, Target: "names:" + scope.Person, What: "Remember a name", Where: "Common knowledge", ScopeKey: "names", Write: true}, err
	}}
	err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, input tools.Input) (tools.Result, error) {
		var in NameChange
		if err := decodeSkill(input.Arguments, &in); err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		receipt, err := p.RememberName(ctx, scope, in)
		if err != nil {
			return tools.Result{State: tools.Failed, Body: json.RawMessage(`{"code":"name_not_recorded"}`)}, nil
		}
		body, err := json.Marshal(struct {
			ID         string `json:"id"`
			Revision   int64  `json:"revision"`
			Remembered bool   `json:"remembered"`
		}{receipt.ID, receipt.Revision, receipt.Remembered()})
		return tools.Result{State: tools.Applied, Body: body, Revision: strconv.FormatInt(receipt.Revision, 10)}, err
	})
	return spec, err
}

func NameDeclaration() provider.ToolDecl {
	return json.RawMessage(`{"type":"function","name":"remember_name","description":"Remember or correct the person's name for a device, organisation, artifact or place. Use an exact quote from the current person's input containing the current name and either the subject or its previous name. Reuse the subject from an existing name item when correcting it. Supply retired_aliases only when explicitly named in that quote. Core shares the name across agents and retains earlier names as retired aliases. Acknowledge only a committed receipt.","parameters":{"type":"object","properties":{"subject":{"type":"string"},"current":{"type":"string"},"quote":{"type":"string"},"retired_aliases":{"type":"array","items":{"type":"string"}}},"required":["subject","current","quote"],"additionalProperties":false}}`)
}
