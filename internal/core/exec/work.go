// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type PrepareWork struct {
	TaskID        string             `json:"task_id"`
	Title         string             `json:"title"`
	Done          string             `json:"done"`
	Workspace     work.WorkspaceSpec `json:"workspace"`
	RequiresApply bool               `json:"requires_apply"`
}
type RetainWork struct {
	TaskID    string `json:"task_id"`
	ReceiptID string `json:"receipt_id"`
	Head      string `json:"head"`
}

func workInput(raw json.RawMessage, name string) (tools.Input, error) {
	var id string
	switch name {
	case "prepare_work":
		var a PrepareWork
		if decodeSkill(raw, &a) != nil || strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Done) == "" {
			return tools.Input{}, tools.ErrInvalid
		}
		id = a.TaskID
	case "retain_work_result":
		var a RetainWork
		if decodeSkill(raw, &a) != nil || a.ReceiptID == "" {
			return tools.Input{}, tools.ErrInvalid
		}
		id = a.TaskID
	default:
		return tools.Input{}, tools.ErrInvalid
	}
	if id == "" {
		return tools.Input{}, tools.ErrInvalid
	}
	return tools.Input{Arguments: raw, Target: "work:" + id, What: "Prepare or preserve task work", Where: "work", ScopeKey: "work:" + id, Write: true, Undoable: true}, nil
}
func RegisterWork(reg *tools.Registry, s *work.Service, agent, conversation string) ([]tools.Spec, error) {
	var specs []tools.Spec
	for _, name := range []string{"prepare_work", "retain_work_result"} {
		spec := tools.Spec{Name: name, Placement: tools.Core, Conc: 1, Deadline: time.Minute, Schema: func(raw json.RawMessage) (tools.Input, error) { return workInput(raw, name) }}
		err := reg.Register(spec, func(ctx context.Context, l lease.Lease, in tools.Input) (tools.Result, error) {
			var value any
			var err error
			if name == "prepare_work" {
				var a PrepareWork
				err = decodeSkill(in.Arguments, &a)
				if err == nil {
					value, err = s.Prepare(ctx, l, work.Task{ID: a.TaskID, AgentID: agent, ConversationID: conversation, Title: a.Title, Done: a.Done, RequiresApply: a.RequiresApply}, a.Workspace)
				}
			} else {
				var a RetainWork
				err = decodeSkill(in.Arguments, &a)
				if err == nil {
					var card work.Card
					card, err = s.Get(ctx, a.TaskID)
					if err == nil && (card.Task.AgentID != agent || card.Task.ConversationID != conversation) {
						err = work.ErrInvalid
					}
					if err == nil {
						value, err = s.Retain(ctx, l, a.TaskID, a.ReceiptID, a.Head)
					}
				}
			}
			if err != nil {
				return tools.Result{State: tools.Failed}, err
			}
			body, err := json.Marshal(value)
			if err != nil {
				return tools.Result{State: tools.Failed}, err
			}
			revision, err := s.Revision(ctx, strings.TrimPrefix(in.Target, "work:"))
			return tools.Result{State: tools.Applied, Body: body, Revision: revision}, err
		})
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
func WorkDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		provider.ToolDecl(`{"type":"function","name":"prepare_work","description":"Prepare a task directory, or a detached Git worktree at a full base commit. This allocates work only; it does not dispatch a worker. The worker creates the named task branch.","parameters":{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"done":{"type":"string"},"requires_apply":{"type":"boolean"},"workspace":{"type":"object","properties":{"repository_id":{"type":"string"},"source":{"type":"string"},"base":{"type":"string"},"branch":{"type":"string"}},"additionalProperties":false}},"required":["task_id","title","done","workspace","requires_apply"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"retain_work_result","description":"Preserve a completed task receipt without a pull request as materials for backup. Requires an existing completed worker receipt; does not verify or apply it. For Git work, head is the exact task branch commit; otherwise head is empty.","parameters":{"type":"object","properties":{"task_id":{"type":"string"},"receipt_id":{"type":"string"},"head":{"type":"string"}},"required":["task_id","receipt_id","head"],"additionalProperties":false}}`),
	}
}
