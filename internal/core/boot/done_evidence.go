// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
)

// Bind the prepared record, including this turn's fill snapshot, once. Each
// operation resolves references against already committed calls before entering
// any writer transaction. The reply stream never passes through this policy.
func (r *conversationRuntime) bindDoneEvidence(ctx context.Context, prepared agents.Preparation, bindings map[string]loop.Tool) error {
	state, err := r.agents.State(ctx, r.agent)
	if err != nil {
		return err
	}
	policy := agents.ProjectWorkMethod(prepared, state)
	for name, tool := range bindings {
		execute := tool.Execute
		tool.Execute = func(ctx context.Context, call provider.ToolCall) loop.Result {
			observed, err := r.doneObservations(ctx)
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			ctx = result.WithCompletionPolicy(ctx, func(request result.CompletionRequest) result.Completion {
				request.Evidence = append([]result.EvidenceReference(nil), request.Evidence...)
				for i := range request.Evidence {
					if request.Evidence[i].TurnID == "" {
						request.Evidence[i].TurnID = prepared.TurnID
					}
				}
				var required []string
				_ = json.Unmarshal(policy.Values["done."+request.WorkKind], &required)
				c := result.AssessCompletion(request, required, func(ref result.EvidenceReference) bool {
					return observed[[2]string{ref.TurnID, ref.CallID}]
				})
				c.PolicyRevision = policy.Revision
				return c
			})
			return execute(ctx, call)
		}
		bindings[name] = tool
	}
	return nil
}

func (r *conversationRuntime) doneObservations(ctx context.Context) (map[[2]string]bool, error) {
	observed := map[[2]string]bool{}
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.turn_id,c.call_id,c.result
 FROM turn_calls c JOIN turn_runs t ON t.id=c.turn_id
 JOIN retract_visible_event p ON p.id='prepare:'||t.id
 JOIN turn_control ctl ON ctl.turn_id=t.id
 JOIN retract_visible_event input ON input.id=ctl.input_event
 WHERE t.conversation=? AND p.agent_id=? AND c.result IS NOT NULL`, r.conversation, r.agent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var turnID, callID string
			var raw []byte
			if err = rows.Scan(&turnID, &callID, &raw); err != nil {
				return err
			}
			var observation loop.Result
			if json.Unmarshal(raw, &observation) == nil && observation.Code == loop.OK && len(observation.Output) > 0 && string(observation.Output) != "null" {
				observed[[2]string{turnID, callID}] = true
			}
		}
		return rows.Err()
	})
	return observed, err
}
