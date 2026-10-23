// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
	"github.com/thisisjun786/lina/kit/tools"
)

type PrepareWork struct {
	WorkKind      string             `json:"work_kind,omitempty"`
	Destination   string             `json:"destination,omitempty"`
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

type ReportWork struct {
	TaskID  string       `json:"task_id"`
	RunID   string       `json:"run_id"`
	Receipt work.Receipt `json:"receipt"`
}
type LandWork struct {
	Completion  result.CompletionRequest `json:"completion,omitempty"`
	TaskID      string                   `json:"task_id"`
	ReceiptID   string                   `json:"receipt_id"`
	Fingerprint string                   `json:"fingerprint"`
	Judgment    string                   `json:"judgment"`
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
	case "report_work_result":
		var a ReportWork
		if decodeSkill(raw, &a) != nil || a.RunID == "" || a.Receipt.RelationshipID != a.TaskID {
			return tools.Input{}, tools.ErrInvalid
		}
		id = a.TaskID
	case "land_work_result":
		var a LandWork
		if decodeSkill(raw, &a) != nil || a.ReceiptID == "" || a.Fingerprint == "" || strings.TrimSpace(a.Judgment) == "" {
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
	for _, name := range []string{"prepare_work", "retain_work_result", "report_work_result", "land_work_result"} {
		class := tools.LocalWrite
		// A model-selected destination is not proof of a private draft boundary.
		if name == "land_work_result" {
			class = tools.Unclassified
		}
		spec := tools.Spec{Classify: tools.Class(class), Name: name, Placement: tools.Core, Conc: 1, Deadline: time.Minute, Schema: func(raw json.RawMessage) (tools.Input, error) { return workInput(raw, name) }}
		err := reg.Register(spec, func(ctx context.Context, l lease.Lease, in tools.Input) (tools.Result, error) {
			var value any
			var err error
			if name == "prepare_work" {
				var a PrepareWork
				err = decodeSkill(in.Arguments, &a)
				if err == nil {
					value, err = s.Prepare(ctx, l, work.Task{WorkKind: a.WorkKind, ID: a.TaskID, AgentID: agent, ConversationID: conversation, Title: a.Title, Done: a.Done, RequiresApply: a.RequiresApply, Destination: a.Destination}, a.Workspace)
				}
			} else if name == "report_work_result" || name == "land_work_result" {
				var card work.Card
				card, err = s.Get(ctx, strings.TrimPrefix(in.Target, "work:"))
				if err == nil && (card.Task.AgentID != agent || card.Task.ConversationID != conversation || card.Task.Destination == "") {
					err = work.ErrInvalid
				}
				if err == nil && name == "report_work_result" {
					var a ReportWork
					err = decodeSkill(in.Arguments, &a)
					// A receipt is a claim, not an adapter completion event. Keep
					// the run reserved until Core has independently observed its end.
					if err == nil && (card.Run == nil || card.Run.ID != a.RunID) {
						err = work.ErrState
					}
					if err == nil && (card.Active || card.TurnStatus == "") {
						err = work.ErrActiveRun
					}
					if err == nil && card.TurnStatus != a.Receipt.TurnRef.TurnStatus {
						err = work.ErrState
					}
					if err == nil {
						err = s.Submit(ctx, a.RunID, a.Receipt)
					}
					if err == nil {
						value, err = s.Get(ctx, a.TaskID)
					}
				} else if err == nil {
					var a LandWork
					err = decodeSkill(in.Arguments, &a)
					if err == nil {
						value, err = s.LandFiles(ctx, a.TaskID, a.ReceiptID, a.Fingerprint, a.Judgment, a.Completion)
					}
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
				problem := map[string]any{"error": err.Error()}
				if name == "report_work_result" && errors.Is(err, work.ErrActiveRun) {
					problem["code"] = "worker_termination_unobserved"
					problem["retryable"] = true
					problem["next_action"] = "wait_for_worker_termination_then_report_again"
				}
				if errors.Is(err, work.ErrFilesChanged) {
					problem["next_action"] = "inspect_result_and_report_done_again"
				}
				body, _ := json.Marshal(problem)
				return tools.Result{State: tools.Failed, Body: body}, err
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
	declarations := []provider.ToolDecl{
		provider.ToolDecl(`{"type":"function","name":"prepare_work","description":"Prepare a task directory, or a detached Git worktree at a full base commit. This allocates work only; it does not dispatch a worker. The worker creates the named task branch.","parameters":{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"done":{"type":"string"},"requires_apply":{"type":"boolean"},"destination":{"type":"string","description":"Absolute destination folder for a non-code result. The worker writes out/ and report.md; Core snapshots them at done and lands a fresh bundle here."},"workspace":{"type":"object","properties":{"repository_id":{"type":"string"},"source":{"type":"string"},"base":{"type":"string"},"branch":{"type":"string"}},"additionalProperties":false}},"required":["task_id","title","done","workspace","requires_apply"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"retain_work_result","description":"Preserve a completed task receipt without a pull request as materials for backup. Requires an existing completed worker receipt; does not verify or apply it. For Git work, head is the exact task branch commit; otherwise head is empty.","parameters":{"type":"object","properties":{"task_id":{"type":"string"},"receipt_id":{"type":"string"},"head":{"type":"string"}},"required":["task_id","receipt_id","head"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"report_work_result","description":"Record a non-code worker receipt only after Core independently records the matching worker termination. A receipt cannot finish an active or unknown run. If termination is unobserved, wait and retry with a fresh tool call. Core fingerprints out/ files and directories and report.md and keeps a snapshot. Changes require reporting done again. This is a claim, not verification.","parameters":{"type":"object","properties":{"task_id":{"type":"string"},"run_id":{"type":"string"},"receipt":{"type":"object"}},"required":["task_id","run_id","receipt"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"land_work_result","description":"After judging the exact done snapshot against the task, land it at its recorded destination. Copies to a new versioned bundle and reads back hashes as file evidence. Changed results require a new done. Partial copies are retained and reported.","parameters":{"type":"object","properties":{"task_id":{"type":"string"},"receipt_id":{"type":"string"},"fingerprint":{"type":"string"},"judgment":{"type":"string"}},"required":["task_id","receipt_id","fingerprint","judgment"],"additionalProperties":false}}`),
	}
	for i, raw := range declarations {
		var decl map[string]any
		_ = json.Unmarshal(raw, &decl)
		props := decl["parameters"].(map[string]any)["properties"].(map[string]any)
		if decl["name"] == "prepare_work" {
			props["work_kind"] = CompletionWorkKindSchema()
		}
		if decl["name"] == "land_work_result" {
			props["completion"] = result.CompletionSchema()
		}
		declarations[i], _ = json.Marshal(decl)
	}
	return declarations
}
func CompletionWorkKindSchema() any {
	return result.CompletionSchema()["properties"].(map[string]any)["work_kind"]
}
