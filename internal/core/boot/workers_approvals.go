// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	acpadapter "github.com/thisisjun786/lina/internal/core/worker/acp"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func (e *workerSession) requestKey(id string) string {
	return fmt.Sprintf("worker-request:%d:%d:%s:%s", e.thread.ProcessGeneration, e.thread.BindingEpoch, e.thread.ID, id)
}

func (e *workerSession) requestApproval(event worker.Event) {
	r := e.origin
	req := event.Approval
	masked, err := secrets.Mask(string(req.Params))
	if err != nil {
		e.bridgeFailure(err)
		return
	}
	// Only the display copy is masked. The adapter retains the original request
	// and permissions; a person's answer never rewrites worker tool arguments.
	summary := "Codex requests additional access"
	switch req.Method {
	case "session/request_permission":
		summary = e.adapter.Name() + " requests approval"
	case "item/commandExecution/requestApproval":
		summary = "Codex wants to run a command"
	case "item/fileChange/requestApproval":
		summary = "Codex wants to change files"
	}
	approval, err := r.personaAuthority.RequestWorkerApproval(r.ctx, authority.Approval{
		TaskID:    e.thread.TaskID,
		EffectKey: e.requestKey(req.ID), Person: r.person, Agent: r.agent, Conversation: r.workerDelivery().conversation,
		Tool: req.Method, What: summary, Where: e.workdir, Target: e.thread.ID,
		Arguments: json.RawMessage(masked.Text()),
	})
	if err != nil {
		e.bridgeFailure(err)
		return
	}
	e.mu.Lock()
	e.approvals[approval.ID] = event
	e.mu.Unlock()
	// Covers an answer racing the initial publication and map insertion.
	r.workers.deliverAnswers(r.ctx)
}

func approvalReply(request worker.Request, choice authority.Choice) (json.RawMessage, error) {
	decision := json.RawMessage(`"decline"`)
	if choice == authority.Once {
		decision = json.RawMessage(`"accept"`)
	}
	switch request.Method {
	case "session/request_permission":
		if choice != authority.Once && choice != authority.Deny {
			return nil, errors.New("unsupported ACP approval choice")
		}
		return acpadapter.PermissionReply(request.Params, choice == authority.Once)
	case "item/commandExecution/requestApproval":
		return json.Marshal(wire.CommandExecutionRequestApprovalResponse{Decision: decision})
	case "item/fileChange/requestApproval":
		return json.Marshal(wire.FileChangeRequestApprovalResponse{Decision: decision})
	case "item/permissions/requestApproval":
		var p wire.PermissionsRequestApprovalParams
		if err := json.Unmarshal(request.Params, &p); err != nil {
			return nil, err
		}
		scope := wire.PermissionGrantScopeTurn
		reply := wire.PermissionsRequestApprovalResponse{Scope: &scope}
		if choice == authority.Once {
			reply.Permissions = wire.GrantedPermissionProfile{FileSystem: p.Permissions.FileSystem, Network: p.Permissions.Network}
		}
		return json.Marshal(reply)
	default:
		return nil, errors.New("unsupported worker approval")
	}
}

func (b *workerRuntime) entries() []*workerSession {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := make([]*workerSession, 0, len(b.sessions))
	for _, entry := range b.sessions {
		entries = append(entries, entry)
	}
	return entries
}

func (b *workerRuntime) deliverAnswers(ctx context.Context) {
	for _, e := range b.entries() {
		e.mu.Lock()
		pending := make(map[string]worker.Event, len(e.approvals))
		for id, event := range e.approvals {
			pending[id] = event
		}
		e.mu.Unlock()
		for id, event := range pending {
			answer, err := e.origin.personaAuthority.ClaimWorkerAnswer(ctx, id)
			if errors.Is(err, authority.ErrStale) {
				continue
			}
			if err != nil {
				e.bridgeFailure(err)
				continue
			}
			e.mu.Lock()
			delete(e.approvals, id)
			e.mu.Unlock()
			raw, err := approvalReply(*event.Approval, answer.Choice)
			if err == nil {
				sendCtx, cancel := context.WithTimeout(e.origin.ctx, 10*time.Second)
				err = e.adapter.Answer(sendCtx, event.Thread, worker.Answer{RequestID: event.Approval.ID, Result: raw})
				cancel()
			}
			// The durable claim precedes sending. A failed send is unknown, never an
			// invitation to send a grant again or to execute the worker's command here.
			if err != nil {
				e.bridgeFailure(err)
			} else {
				e.answerDelivered(ctx, event.Thread)
			}
		}
	}
}

func (e *workerSession) stopRequest(requestID string) { e.stopMatching("", requestID) }
func (e *workerSession) stopRequests(turnID string)   { e.stopMatching(turnID, "") }
func (e *workerSession) stopMatching(turnID, requestID string) {
	e.mu.Lock()
	var ids, questionIDs []string
	for id, event := range e.approvals {
		if (turnID == "" || event.Thread.TurnID == turnID) && (requestID == "" || event.Approval.ID == requestID) {
			ids = append(ids, id)
			delete(e.approvals, id)
		}
	}
	for id, q := range e.questions {
		if q.kind == "recovery" {
			continue
		}
		if (turnID == "" || q.event.Thread.TurnID == turnID) && (requestID == "" || q.requestID == requestID) && q.card.Card.Outcome == nil {
			outcome := stoppedQuestionOutcome
			q.card.Card.Outcome = &outcome
			questionIDs = append(questionIDs, id)
		}
	}
	e.mu.Unlock()
	for _, id := range questionIDs {
		_ = e.origin.router.root.decisions.PublishQuestion(e.origin.ctx, id)
	}
	for _, id := range ids {
		_ = e.origin.personaAuthority.StopWorkerApproval(e.origin.ctx, id)
	}
}
func (e *workerSession) bridgeFailure(err error) {
	name := "Codex"
	if _, ok := e.adapter.(*acpadapter.Adapter); ok {
		name = e.adapter.Name()
	}
	_ = e.origin.workerNotice(e.origin.ctx, e.input, e.requestKey(fmt.Sprintf("error:%d", time.Now().UnixNano())), name+" request could not be delivered: "+err.Error())
}

// A scheduled task resumes only after all pending requests have been answered.
func (e *workerSession) answerDelivered(ctx context.Context, thread worker.Thread) {
	if e.answered == nil {
		return
	}
	e.mu.Lock()
	pending := len(e.approvals) > 0
	for _, q := range e.questions {
		pending = pending || q.card.Card.Outcome == nil
	}
	e.mu.Unlock()
	if !pending {
		e.answered(ctx, thread)
	}
}
