// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type PlanDraftRequest struct {
	RootID       string `json:"root_id"`
	ResultID     string `json:"result_id"`
	BaseRevision int    `json:"base_revision"`
	Instruction  string `json:"instruction"`
}
type QueuedPlan struct {
	JobID    string `json:"job_id"`
	Sequence int64  `json:"sequence"`
}
type PlanQueue func(context.Context, PlanDraftRequest) (QueuedPlan, error)

// RegisterPlanning can only queue a draft. Confirmation is deliberately absent
// from the model's tools and is admitted through the authenticated client.
func RegisterPlanning(registry *tools.Registry, queue PlanQueue) (tools.Spec, error) {
	spec := tools.Spec{Name: "draft_plan", Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var p PlanDraftRequest
		if len(raw) > 32768 || decodeSkill(raw, &p) != nil || p.RootID == "" || (p.ResultID == "") != (p.BaseRevision == 0) || p.BaseRevision < 0 || len(p.Instruction) > 16384 {
			return tools.Input{}, tools.ErrInvalid
		}
		return tools.Input{Arguments: output(p), Target: "plan:" + p.RootID, What: "draft_plan", Where: "Plans", ScopeKey: "planning", Write: true, Undoable: true}, nil
	}}
	err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		var p PlanDraftRequest
		_ = json.Unmarshal(in.Arguments, &p)
		queued, err := queue(ctx, p)
		if err != nil {
			return tools.Result{State: tools.Failed, Body: refusal(err)}, nil
		}
		return tools.Result{State: tools.Applied, Revision: strconv.FormatInt(queued.Sequence, 10), Body: output(struct {
			QueuedPlan
			State string `json:"state"`
		}{queued, "queued"})}, nil
	})
	return spec, err
}
func PlanningDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"plan_index","description":"List accepted goals and projects and the current plan preview ids and revisions. Discover the root id here before drafting; previews create no plan records.","parameters":{"type":"object","properties":{},"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"read_plan","description":"Read the complete current plan preview by result id before replacing it. Only the person can confirm plans.","parameters":{"type":"object","properties":{"result_id":{"type":"string"}},"required":["result_id"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"draft_plan","description":"Ask Klotho to draft or replace the complete plan for an already accepted goal or project record. Use the record id returned by the user's acceptance. For replacement supply the result id and latest revision; omitted children are removed. This queues background planning and creates no plan records or execution. Only the person can confirm the resulting plan card.","parameters":{"type":"object","properties":{"root_id":{"type":"string"},"result_id":{"type":"string"},"base_revision":{"type":"integer","minimum":0},"instruction":{"type":"string"}},"required":["root_id","result_id","base_revision","instruction"],"additionalProperties":false}}`)}
}

// RegisterPlanReads exposes metadata discovery and explicit body reads. Preview
// bodies never enter model history merely because a worker published a card.
func RegisterPlanReads(registry *tools.Registry, index func(context.Context) (json.RawMessage, error), read func(context.Context, string) (json.RawMessage, error)) ([]tools.Spec, error) {
	var specs []tools.Spec
	for _, name := range []string{"plan_index", "read_plan"} {
		spec := tools.Spec{Name: name, Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
			id := ""
			if name == "plan_index" {
				if decodeSkill(raw, &struct{}{}) != nil {
					return tools.Input{}, tools.ErrInvalid
				}
			} else {
				var p struct {
					ID string `json:"result_id"`
				}
				if decodeSkill(raw, &p) != nil || p.ID == "" {
					return tools.Input{}, tools.ErrInvalid
				}
				id = p.ID
			}
			return tools.Input{Arguments: raw, Target: "plan:" + id, What: name, Where: "Plans", ScopeKey: "planning"}, nil
		}}
		err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
			var raw json.RawMessage
			var err error
			if name == "plan_index" {
				raw, err = index(ctx)
			} else {
				var p struct {
					ID string `json:"result_id"`
				}
				_ = json.Unmarshal(in.Arguments, &p)
				raw, err = read(ctx, p.ID)
			}
			if err != nil {
				return tools.Result{State: tools.Failed, Body: refusal(err)}, nil
			}
			return tools.Result{State: tools.Applied, Body: raw}, nil
		})
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
