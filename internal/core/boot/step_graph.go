// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/thisisjun786/lina/internal/core/canon"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type stepGraphInput struct {
	GoalID   string      `json:"goal_id"`
	Expected *uint64     `json:"expected_revision"`
	Steps    []work.Step `json:"steps"`
}

func decodeStepGraph(raw string, target any) error {
	if len(raw) > 256<<10 {
		return work.GraphInvalid
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return work.GraphInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return work.GraphInvalid
	}
	return nil
}
func graphResult(id string, graph work.StepGraph, err error) loop.Result {
	if err != nil {
		code := work.GraphUnavailable
		var refusal work.GraphError
		if errors.As(err, &refusal) {
			code = refusal
		}
		body, _ := json.Marshal(map[string]string{"code": string(code)})
		return loop.Result{ID: id, Code: loop.Invalid, Output: body}
	}
	body, _ := json.Marshal(graph)
	return loop.Result{ID: id, Code: loop.OK, Output: body}
}
func (r *conversationRuntime) prepareStepGraphTools(engine *work.Engine, bindings map[string]loop.Tool) []provider.ToolDecl {
	decode := func(raw string) (stepGraphInput, error) {
		var in stepGraphInput
		err := decodeStepGraph(raw, &in)
		if in.GoalID == "" || in.Expected == nil || in.Steps == nil {
			err = work.GraphInvalid
		}
		return in, err
	}
	bindings["submit_step_graph"] = loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			in, err := decode(c.Arguments)
			recordID, ok := work.ProjectRecordID(in.GoalID)
			if err == nil && !ok {
				err = work.ErrInvalid
			}
			return recordID, err
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			in, err := decode(c.Arguments)
			if err != nil {
				return graphResult(c.ID, work.StepGraph{}, err)
			}
			owner, conversation, err := r.stepGraphOwner(ctx, in.GoalID)
			if err != nil {
				return graphResult(c.ID, work.StepGraph{}, err)
			}
			graph, err := engine.SubmitStepGraph(ctx, in.GoalID, r.person, owner, conversation, *in.Expected, in.Steps)
			return graphResult(c.ID, graph, err)
		},
	}
	read := func(raw string) (string, error) {
		var in struct {
			GoalID string `json:"goal_id"`
		}
		err := decodeStepGraph(raw, &in)
		if in.GoalID == "" {
			err = work.GraphInvalid
		}
		return in.GoalID, err
	}
	bindings["read_step_graph"] = loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			id, err := read(c.Arguments)
			recordID, ok := work.ProjectRecordID(id)
			if err == nil && !ok {
				err = work.ErrInvalid
			}
			return recordID, err
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			id, err := read(c.Arguments)
			if err != nil {
				return graphResult(c.ID, work.StepGraph{}, err)
			}
			owner, conversation, err := r.stepGraphOwner(ctx, id)
			if err != nil {
				return graphResult(c.ID, work.StepGraph{}, err)
			}
			graph, err := engine.ReadStepGraph(ctx, id, r.person, owner, conversation)
			return graphResult(c.ID, graph, err)
		},
	}
	return []provider.ToolDecl{
		provider.ToolDecl(`{"type":"function","name":"submit_step_graph","description":"Submit the complete goal step graph. Kind defaults to task (base, area, worker and resources required). A background step needs a start_run brief and resources. A wait needs either at (Unix seconds), or record_id, revision and deadline; it holds no worker slot. Use its latest graph revision (0 before the first graph). Give each step a plain name of at most 24 display cells, without issue numbers, branches, paths, check names or ids. Core admits or refuses the whole graph and runs eligible work. Started steps may be renamed but their work and blockers must stay. Grants no effect authority. Read the graph after a stale revision.","parameters":{"type":"object","properties":{"goal_id":{"type":"string"},"expected_revision":{"type":"integer","minimum":0},"steps":{"type":"array","minItems":1,"maxItems":256,"items":{"type":"object","properties":{"kind":{"type":"string","enum":["task","background","wait"]},"background":{"type":"object"},"wait":{"type":"object","properties":{"at":{"type":"integer"},"record_id":{"type":"string"},"revision":{"type":"string"},"deadline":{"type":"integer"}},"additionalProperties":false},"id":{"type":"string"},"name":{"type":"string"},"done":{"type":"string"},"repository":{"type":"string"},"base":{"type":"string"},"area":{"type":"string"},"worker":{"type":"string"},"resources":{"type":"object","properties":{"cpu":{"type":"number"},"memory_mb":{"type":"integer"},"disk_mb":{"type":"integer"}},"required":["cpu","memory_mb","disk_mb"],"additionalProperties":false},"depends_on":{"type":"array","items":{"type":"string"}}},"required":["id","name","done","repository","depends_on"],"additionalProperties":false}}},"required":["goal_id","expected_revision","steps"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"read_step_graph","description":"Read the last admitted step graph and its revision for this manager's code goal. Returns the exact saved names, work and blockers, including after restart. Revision 0 means no explicit graph revision has been submitted.","parameters":{"type":"object","properties":{"goal_id":{"type":"string"}},"required":["goal_id"],"additionalProperties":false}}`),
	}
}

// A relayed coding goal remains accessible to its recorded originating
// conversation and its owning manager, never an arbitrary same-person agent.
func (r *conversationRuntime) stepGraphOwner(ctx context.Context, id string) (string, string, error) {
	owner, conversation := r.agent, r.conversation
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		a, err := readCoding(ctx, tx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.Origin.Person != r.person || !((a.Owner == r.agent && a.Conversation == r.conversation) || (a.Origin.Agent == r.agent && a.Origin.Conversation == r.conversation)) {
			return work.GraphUnavailable
		}
		owner, conversation = a.Owner, a.Conversation
		return nil
	})
	return owner, conversation, err
}
