// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type RelationshipChange struct {
	Quote string `json:"quote"`
}

// RecordRelationship stores an extractive observation of the current native
// owner event. No model-authored intimacy label or summary is accepted. It uses
// the ordinary Moirai memory writer/resolver and never touches persona state.
func (p *Persona) RecordRelationship(ctx context.Context, scope PersonaScope, in RelationshipChange) (RememberReceipt, error) {
	event, err := p.confirmed(ctx, scope, in.Quote)
	if err != nil {
		return RememberReceipt{}, err
	}
	request := memory.Write{Agent: scope.Agent, Person: scope.Person, Item: memory.Item{Kind: memory.Relationship, Content: in.Quote, Subject: memory.Subject{Kind: memory.Person, ID: scope.Person}, Basis: memory.Stated, ValidFrom: event.OccurredAt.Unix(), Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}}}
	resolve, err := p.memory.prepare(ctx, request)
	if err != nil {
		return RememberReceipt{}, err
	}
	var item memory.Item
	err = p.memory.write(ctx, func(tx *canon.Tx) error {
		// Select and supersede in the same fenced transaction, so simultaneous
		// first observations cannot create two independent current views.
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT record FROM retract_visible_memory m WHERE person=? AND holder=? AND subject_kind='person' AND subject_id=? AND json_extract(record,'$.kind')='relationship' AND state='active'
   AND valid_from<=? AND (valid_until=0 OR valid_until>?) AND NOT EXISTS(SELECT 1 FROM memory_item n WHERE n.supersedes=m.id)
   ORDER BY sequence DESC LIMIT 1`, scope.Person, scope.Agent, scope.Person, p.now().Unix(), p.now().Unix()).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			var old memory.Item
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if old.Content == in.Quote {
				// Still revalidate the current event inside the commit boundary.
				if _, err := resolve(ctx, tx, request.Item.Evidence[0]); err != nil {
					return err
				}
				item = old
				return nil
			}
			if old.ValidFrom > request.Item.ValidFrom {
				return memory.ErrSupersedes
			}
			request.Item.Supersedes = old.ID
		}
		item, err = memory.Insert(ctx, tx, request, resolve)
		return err
	})
	if err != nil {
		return RememberReceipt{}, err
	}
	return RememberReceipt{ID: item.ID, Revision: item.Revision, committed: true}, nil
}

func RegisterRelationship(registry *tools.Registry, p *Persona, scope PersonaScope) (tools.Spec, error) {
	spec := tools.Spec{Name: "record_relationship", Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var in RelationshipChange
		if err := decodeSkill(raw, &in); err != nil || strings.TrimSpace(in.Quote) == "" || len(in.Quote) > 8192 {
			return tools.Input{}, tools.ErrInvalid
		}
		args, err := json.Marshal(in)
		return tools.Input{Arguments: args, Target: "relationship:" + scope.Agent, What: "Record a shared relationship observation", Where: "This agent's personal memory", ScopeKey: "relationship", Write: true}, err
	}}
	err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, input tools.Input) (tools.Result, error) {
		var in RelationshipChange
		if err := decodeSkill(input.Arguments, &in); err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		receipt, err := p.RecordRelationship(ctx, scope, in)
		if err != nil {
			return tools.Result{State: tools.Failed, Body: json.RawMessage(`{"code":"relationship_not_recorded"}`)}, nil
		}
		body, err := json.Marshal(struct {
			ID         string `json:"id"`
			Revision   int64  `json:"revision"`
			Remembered bool   `json:"remembered"`
		}{receipt.ID, receipt.Revision, receipt.Remembered()})
		return tools.Result{State: tools.Applied, Body: body, Revision: "1"}, err
	})
	return spec, err
}

func RelationshipDeclaration() provider.ToolDecl {
	return json.RawMessage(`{"type":"function","name":"record_relationship","description":"Record this agent's view of a shared experience, relationship or boundary explicitly stated in the current person's input. Supply an exact quote of the relevant statement. The observation belongs only to this agent and person, and supersedes its previous relationship view with append-only evidence. Never infer intimacy from emotion or imagined events. Do not call for unrelated statements. This is silent personal memory, not persona emotion or permission.","parameters":{"type":"object","properties":{"quote":{"type":"string"}},"required":["quote"],"additionalProperties":false}}`)
}
