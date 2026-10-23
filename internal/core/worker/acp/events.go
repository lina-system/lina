// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
)

func (a *Adapter) prompt(th worker.Thread, replies <-chan wire.PromptResult) {
	defer a.turns.Done()
	c := a.client
	updates, permissions, requests := c.Updates(), c.Permissions(), c.Requests()
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			if !a.update(th, u) {
				return
			}
		case p, ok := <-permissions:
			if !ok {
				permissions = nil
				continue
			}
			if p != nil {
				a.finish(th, worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "approval_unsupported"})
				return
			}
		case r, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			if r != nil {
				a.finish(th, worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "question_unsupported"})
				return
			}
		case result := <-replies:
			// The client's read loop enqueues updates before delivering the reply.
			// Drain that prefix before emitting the terminal event.
			for {
				select {
				case u, ok := <-updates:
					if !ok {
						updates = nil
						goto drained
					}
					if !a.update(th, u) {
						return
					}
				default:
					goto drained
				}
			}
		drained:
			if result.Err != nil {
				a.finish(th, worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "prompt_failed"})
				return
			}
			a.mu.Lock()
			a.result.StopReason = string(result.Response.StopReason)
			cancelled := a.cancelled
			a.mu.Unlock()
			f := worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: string(result.Response.StopReason)}
			switch result.Response.StopReason {
			case wire.StopReasonEndTurn:
				f = worker.Finish{Status: worker.Done}
			case wire.StopReasonCancelled:
				f = worker.Finish{Status: worker.Interrupted, Code: "worker_cancelled"}
				if cancelled {
					f.Code = "cancelled"
				}
			case wire.StopReasonRefusal, wire.StopReasonMaxTokens, wire.StopReasonMaxTurnRequests:
			}
			a.finish(th, f)
			return
		case <-a.life.Done():
			return
		}
	}
}
func (a *Adapter) update(th worker.Thread, u wire.Update) bool {
	if u.Replay || string(u.Notification.SessionID) != th.ID {
		return true
	}
	v := u.Notification.Update
	p := worker.Progress{}
	switch {
	case v.AgentMessageChunk != nil:
		p.Phase = "message"
		if v.AgentMessageChunk.Content.Text != nil {
			p.Text = v.AgentMessageChunk.Content.Text.Text
		}
		a.mu.Lock()
		a.result.Output += p.Text
		a.mu.Unlock()
	case v.AgentThoughtChunk != nil:
		p.Phase = "thought"
		if v.AgentThoughtChunk.Content.Text != nil {
			p.Text = v.AgentThoughtChunk.Content.Text.Text
		}
	case v.ToolCall != nil:
		p.Phase = "tool_call"
		p.ItemID = string(v.ToolCall.ToolCallID)
		p.Text = v.ToolCall.Title
	case v.ToolCallUpdate != nil:
		p.Phase = "tool_call_update"
		p.ItemID = string(v.ToolCallUpdate.ToolCallID)
		if v.ToolCallUpdate.Title != nil {
			p.Text = *v.ToolCallUpdate.Title
		}
		if p.Text == "" && v.ToolCallUpdate.Status != nil {
			p.Text = string(*v.ToolCallUpdate.Status)
		}
	case v.Plan != nil:
		p.Phase = "plan"
		var entries []string
		for _, e := range v.Plan.Entries {
			entries = append(entries, fmt.Sprintf("%s: %s", e.Status, e.Content))
		}
		p.Text = strings.Join(entries, "; ")
	case len(v.Unrecognized) > 0:
		a.mu.Lock()
		a.result.Unknown = append(a.result.Unknown, append(json.RawMessage(nil), v.Unrecognized...))
		a.mu.Unlock()
		return true
	default:
		return true
	}
	return a.emit(worker.Event{Thread: th, Kind: worker.ProgressEvent, Progress: &p})
}
func (a *Adapter) finish(th worker.Thread, f worker.Finish) {
	a.mu.Lock()
	a.state = worker.Idle
	if f.Failure == worker.ConnectionFailure {
		a.state = worker.Dead
	}
	a.mu.Unlock()
	a.emit(worker.Event{Thread: th, Kind: worker.FinishEvent, Finish: &f})
}
func (a *Adapter) emit(e worker.Event) bool {
	select {
	case a.events <- e:
		return true
	case <-a.life.Done():
		return false
	}
}
