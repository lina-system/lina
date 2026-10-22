// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type workTaskInput struct {
	ID         string         `json:"id"`
	Done       string         `json:"done"`
	Repository string         `json:"repository"`
	Base       string         `json:"base"`
	Area       string         `json:"area"`
	Worker     string         `json:"worker"`
	Resources  work.Resources `json:"resources"`
	DependsOn  []string       `json:"depends_on,omitempty"`
}
type workInput struct {
	work.Definition
	ServiceTarget   string          `json:"service_target,omitempty"`
	NoServiceReason string          `json:"no_service_reason,omitempty"`
	Tasks           []workTaskInput `json:"tasks"`
}

func decodeWork(raw string) (workInput, error) {
	var in workInput
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return in, work.ErrInvalid
	}
	if len(raw) > 256<<10 || in.Outcome == "" || in.Done == "" || len(in.Criteria) == 0 || len(in.Decisions) > 0 {
		return in, work.ErrInvalid
	}
	return in, nil
}
func (r *conversationRuntime) prepareGoalWorkTools(turnID string, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	runtime := r.workEngine
	if runtime == nil && r.router != nil {
		runtime = r.router.root.workEngine
	}
	if runtime == nil {
		return nil, nil
	}
	bindings["dispatch_code_goal"] = loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			_, err := decodeWork(c.Arguments)
			return "work:" + r.agent, err
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			in, err := decodeWork(c.Arguments)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Invalid}
			}
			tasks := []work.Task{}
			for _, t := range in.Tasks {
				tasks = append(tasks, work.Task{ID: t.ID, Done: t.Done, Repository: t.Repository, Base: t.Base, Area: t.Area, Worker: t.Worker, Resources: t.Resources, DependsOn: t.DependsOn, Stage: work.TaskReady})
			}
			// Identity, execution state, criterion evidence and authority are never
			// accepted from the model. Later effects are authorized by the driver.
			goal := work.Goal{ID: turnID + ":" + c.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID, Summary: in.Outcome, Done: in.Done, Scope: in.Scope, Assumptions: in.Assumptions, Criteria: in.Criteria, ServiceTarget: in.ServiceTarget, NoServiceReason: in.NoServiceReason, Tasks: tasks}
			if err = runtime.engine.Admit(ctx, goal); err != nil {
				return loop.Result{ID: c.ID, Code: loop.Invalid, Output: json.RawMessage(`{"code":"work_admission_failed"}`)}
			}
			body, _ := json.Marshal(map[string]string{"goal_id": goal.ID, "state": "running"})
			return loop.Result{ID: c.ID, Code: loop.OK, Output: body}
		},
	}
	return []provider.ToolDecl{provider.ToolDecl(`{"type":"function","name":"dispatch_code_goal","description":"Hand a code goal to the work engine through merge, QA, deployment if applicable and runtime verification. Define the complete outcome and checked criteria, scope, assumptions and dependency-ordered tasks; return immediately. Admission grants no external-effect authority.","parameters":{"type":"object","properties":{"outcome":{"type":"string"},"done":{"type":"string"},"scope":{"type":"object","properties":{"repositories":{"type":"array","items":{"type":"string"}},"includes":{"type":"string"},"excludes":{"type":"string"}},"required":["repositories","includes","excludes"],"additionalProperties":false},"assumptions":{"type":"array","items":{"type":"string"}},"criteria":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"statement":{"type":"string"},"check":{"type":"object","properties":{"kind":{"type":"string","enum":["read","check","judgment"]},"method":{"type":"string"},"target":{"type":"string"},"task_id":{"type":"string"},"rubric":{"type":"string"}},"required":["kind","method","target"],"additionalProperties":false}},"required":["id","statement","check"],"additionalProperties":false}},"service_target":{"type":"string"},"no_service_reason":{"type":"string"},"tasks":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"done":{"type":"string"},"repository":{"type":"string"},"base":{"type":"string"},"area":{"type":"string"},"worker":{"type":"string"},"resources":{"type":"object","properties":{"cpu":{"type":"number"},"memory_mb":{"type":"integer"},"disk_mb":{"type":"integer"}},"required":["cpu","memory_mb","disk_mb"],"additionalProperties":false},"depends_on":{"type":"array","items":{"type":"string"}}},"required":["id","done","repository","base","area","worker","resources"],"additionalProperties":false}}},"required":["outcome","done","scope","criteria","tasks"],"additionalProperties":false}}`)}, nil
}
