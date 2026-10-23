// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

// Reopen only the Core observations already supplied in this manager's handoff.
// No model-supplied task, path or command can expand the assignment's scope.
func (r *conversationRuntime) codingDeliveryEvidence(ctx context.Context) (json.RawMessage, error) {
	c := r.coding
	if c == nil || c.Phase != "delivery" || c.Result == nil || c.Result.PR == nil || c.Review == nil {
		return nil, work.ErrState
	}
	a, d, rr := c.Assignment, c.Result, c.Review
	if a.Owner != r.agent || a.Origin.Person != r.person || a.Conversation != r.conversation || d.ID != a.ID || d.Owner != a.Owner || d.Manager != a.Conversation || d.Origin.Person != a.Origin.Person || rr.Task != a.ID || rr.Head != d.PR.Head || d.Code != "ready_for_review" {
		return nil, work.ErrState
	}
	card, err := r.work.Get(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if card.Active || card.Receipt == nil || card.Run == nil || card.Task.AgentID != r.agent || card.Task.PersonID != r.person || card.Task.ConversationID != r.conversation || card.Head != rr.Head || card.CompletionHead != rr.Head || card.Receipt.EventID != rr.Receipt || card.Run.Generation != rr.Generation {
		return nil, work.ErrState
	}
	if err = d.PR.Verify(rr.Repository, d.TaskDefinition().Branch, d.Definition.Base, rr.Head); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"source": "core_observation", "task_id": a.ID, "receipt_id": rr.Receipt,
		"head": rr.Head, "pull_request": d.PR, "base_commit": d.BaseCommit,
		"changed_files": d.ChangedFiles, "design_at_base": d.DesignText, "diff": d.ChangeEvidence,
	})
}

func (r *conversationRuntime) prepareCodingEvidence(turnID string, bindings map[string]loop.Tool) (provider.ToolDecl, error) {
	target := "coding:" + r.coding.Assignment.ID + ":delivery"
	spec := tools.Spec{Name: "read_coding_delivery", Classify: tools.Class(tools.Research), Conc: 1, Deadline: time.Minute, Placement: tools.Core, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var args struct{}
		if decodeWorkerArgs(raw, &args) != nil {
			return tools.Input{}, tools.ErrInvalid
		}
		return tools.Input{Arguments: json.RawMessage(`{}`), Target: target, What: "Read code delivery evidence", Where: r.conversation, ScopeKey: "coding-manager"}, nil
	}}
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	if err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, _ tools.Input) (tools.Result, error) {
		body, err := r.codingDeliveryEvidence(ctx)
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		return tools.Result{State: tools.Applied, Body: body}, nil
	}); err != nil {
		return nil, err
	}
	auth := grant.Request{GrantID: turnID + ":coding-evidence", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "coding-manager", Revision: "unrecorded"}
	r.personaMu.Lock()
	r.personaGrants[auth.GrantID] = grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: spec.Name, Target: target}}, Revisions: map[string]string{target: auth.Revision}}
	r.personaMu.Unlock()
	bindings[spec.Name] = turn.Tool(registry, spec, tools.Call{Auth: auth}, turnID)
	return provider.ToolDecl(`{"type":"function","name":"read_coding_delivery","description":"Read the retained Core observations for this delivery: exact head, PR checks, reviews, diff and base design. No command is rerun. Use a completion reference only when these observations establish the required evidence kind; a green PR alone does not prove the owner path ran.","parameters":{"type":"object","properties":{},"additionalProperties":false}}`), nil
}
