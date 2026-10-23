// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// A caller binding cannot delegate the OS user's installed-worker authority.
// The owner sees and approves this exact task before any provider/tool dispatch.
// This reuses the native Once/Deny decision path and its epoch/identity checks.
func (p *publicationRuntime) approvalRequest(rec a2a.Record) (authority.Approval, error) {
	if len(rec.Task.History) == 0 {
		return authority.Approval{}, a2a.ErrInvalid
	}
	args, err := json.Marshal(map[string]any{"publication": rec.Publication, "caller": rec.Principal.Subject, "client": rec.Principal.Client, "task_id": rec.Task.ID, "input": rec.Task.History[0], "definition": rec.Definition, "binding": rec.Authority, "deadline": rec.Deadline})
	if err != nil {
		return authority.Approval{}, err
	}
	masked, err := secrets.Mask(string(args))
	if err != nil || masked.Text() != string(args) {
		return authority.Approval{}, a2a.ErrInvalid
	}
	return authority.Approval{TaskID: "a2a:" + rec.Task.ID, EffectKey: "a2a-owner-execution:" + rec.Task.ID, Person: p.root.person, Agent: rec.Agent, Conversation: rec.Conversation, Tool: "a2a_worker_execution", Target: rec.Definition.Work.Repository, Where: rec.Definition.Work.Repository, What: "Run this external task? The installed worker uses your files, login and integrations. Review the full task input and scope.", Arguments: args, Network: true, Outward: true}, nil
}
func (p *publicationRuntime) approved(ctx context.Context, rec a2a.Record) error {
	if rec.OwnerApproval == "" {
		return a2a.ErrDenied
	}
	wanted, err := p.approvalRequest(rec)
	if err != nil {
		return err
	}
	granted, err := p.root.personaAuthority.Approval(ctx, rec.OwnerApproval)
	if err != nil {
		return err
	}
	if granted.State != authority.Consumed || granted.Choice != authority.Once || granted.Epoch != p.root.epoch || granted.AnsweredBy != p.root.person || granted.Person != wanted.Person || granted.Agent != wanted.Agent || granted.TaskID != wanted.TaskID || granted.EffectKey != wanted.EffectKey || granted.Tool != wanted.Tool || granted.Conversation != wanted.Conversation || !a2a.ArgumentsEqual(granted.Arguments, wanted.Arguments) {
		return a2a.ErrDenied
	}
	return nil
}
func (p *publicationRuntime) approve(ctx context.Context, rec a2a.Record) (bool, error) {
	if rec.OwnerApproval != "" && p.approved(ctx, rec) == nil {
		return true, nil
	}
	wanted, err := p.approvalRequest(rec)
	if err != nil {
		return false, err
	}
	answer, err := p.root.personaAuthority.RequestWorkerApproval(ctx, wanted)
	if err != nil {
		return false, err
	}
	if rec.OwnerApproval == "" {
		rec, err = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
			current.OwnerApproval = answer.ID
			a2a.SetState(current, a2a.InputRequired, time.Now())
			return nil
		})
		if err != nil {
			return false, err
		}
	}
	if answer.State == authority.Pending {
		return false, nil
	}
	if answer.State != authority.Approved || answer.Choice != authority.Once {
		return false, a2a.ErrDenied
	}
	if _, err = p.root.personaAuthority.ClaimWorkerAnswer(ctx, answer.ID); err != nil {
		return false, err
	}
	return true, p.approved(ctx, rec)
}
