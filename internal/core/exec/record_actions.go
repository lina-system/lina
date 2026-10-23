// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type RecordAction struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Handle   string `json:"handle,omitempty"`
}

func registerRecordActions(registry *tools.Registry, store *records.Store, actor records.Actor) ([]tools.Spec, error) {
	var specs []tools.Spec
	for _, name := range []string{"promote_memo", "undo_record"} {
		spec := tools.Spec{Name: name, Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Classify: func(tools.Input) tools.EffectClass { return tools.PrivateDraft }, Schema: func(raw json.RawMessage) (tools.Input, error) {
			var a RecordAction
			if decodeSkill(raw, &a) != nil || a.ID == "" || a.Revision == "" || (name == "undo_record") != (a.Handle != "") {
				return tools.Input{}, tools.ErrInvalid
			}
			args, _ := json.Marshal(a)
			return tools.Input{Arguments: args, Target: "record:" + a.ID, What: name, Where: "Records", ScopeKey: "records", Write: true, Undoable: true}, nil
		}}
		err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
			var a RecordAction
			_ = json.Unmarshal(in.Arguments, &a)
			var applied records.Applied
			var err error
			if name == "promote_memo" {
				applied, err = store.PromoteMemo(ctx, actor, a.ID, a.Revision)
			} else {
				applied, err = store.UndoRecord(ctx, actor, a.Handle, a.ID, a.Revision)
			}
			if err != nil {
				return RecordFailure(err), nil
			}
			return tools.Result{State: tools.Applied, Body: output(applied), Revision: applied.Revision}, nil
		})
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func recordActionDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"promote_memo","description":"Convert a saved memo to a to-do when its intent is clear. Keeps its id, path and text. Use the latest read_record revision. Returns a saved version and one-action undo handle.","parameters":{"type":"object","properties":{"id":{"type":"string"},"revision":{"type":"string"}},"required":["id","revision"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"undo_record","description":"Undo a memo promotion or automatic to-do addition using its receipt handle and current record revision. Later edits are never overwritten.","parameters":{"type":"object","properties":{"id":{"type":"string"},"revision":{"type":"string"},"handle":{"type":"string"}},"required":["id","revision","handle"],"additionalProperties":false}}`),
	}
}
