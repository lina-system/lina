// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
			if p != nil && !a.permission(th, p) {
				return
			}
		case r, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			if r != nil && !a.question(th, r) {
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
			// Requests are enqueued before the prompt reply. They are already expired
			// in the shared client and must not become requests of the next turn.
			for len(permissions) > 0 {
				<-permissions
			}
			for len(requests) > 0 {
				<-requests
			}
			if result.Err != nil {
				// An error reply observes termination, not the outcome of its
				// effects. Later prompts cannot reconcile that uncertainty.
				a.mu.Lock()
				a.uncertainEffects = true
				a.mu.Unlock()
				failure := worker.ConnectionFailure
				var remote *wire.RPCError
				if errors.As(result.Err, &remote) {
					// A complete error reply ends the turn even if EOF follows.
					// Retain neither its numeric code nor remote message/data.
					failure = worker.WorkerFailure
				}
				a.finish(th, worker.Finish{Status: worker.Failed, Failure: failure, Code: "prompt_failed"})
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
				a.mu.Lock()
				if a.extensionFailure != nil {
					f = *a.extensionFailure
				}
				a.mu.Unlock()
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
	a.mu.Lock()
	raw, _ := json.Marshal(v)
	a.doing.Record("acp", raw, time.Now())
	a.mu.Unlock()
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
		a.observeNativeTool(string(v.ToolCall.ToolCallID), v.ToolCall.Status, true)
		p.Phase = "tool_call"
		p.ItemID = string(v.ToolCall.ToolCallID)
		p.Text = v.ToolCall.Title
	case v.ToolCallUpdate != nil:
		a.observeNativeTool(string(v.ToolCallUpdate.ToolCallID), v.ToolCallUpdate.Status, false)
		p.Phase = "tool_call_update"
		p.ItemID = string(v.ToolCallUpdate.ToolCallID)
		if v.ToolCallUpdate.Title != nil {
			p.Text = *v.ToolCallUpdate.Title
		}
		if p.Text == "" && v.ToolCallUpdate.Status != nil {
			p.Text = string(*v.ToolCallUpdate.Status)
		}
	case v.SessionInfoUpdate != nil:
		a.sessionFailure(v.SessionInfoUpdate.Meta)
		p.Phase = "doing"
	case v.Plan != nil:
		p.Phase = "plan"
		var entries []string
		for _, e := range v.Plan.Entries {
			entries = append(entries, fmt.Sprintf("%s: %s", e.Status, e.Content))
		}
		p.Text = strings.Join(entries, "; ")
	case v.UsageUpdate != nil:
		a.vendorQuota(v.UsageUpdate.Meta)
		return true
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
	a.requests = nil
	a.lastFinish = &f
	if a.state == worker.Active || a.state == worker.Waiting {
		close(a.turnEnded)
	}
	a.state = worker.Idle
	if f.Failure == worker.ConnectionFailure {
		a.state = worker.Dead
	}
	a.mu.Unlock()
	a.emit(worker.Event{Thread: th, Kind: worker.FinishEvent, Finish: &f})
}
func (a *Adapter) emit(e worker.Event) bool {
	a.mu.Lock()
	if a.state == worker.Active || a.state == worker.Idle || e.Finish != nil {
		a.doing.Busy(a.state == worker.Active && e.Finish == nil, time.Now())
	}
	observation := a.doing.Snapshot()
	if !observation.Since.IsZero() {
		e.Doing = &observation
	}
	a.mu.Unlock()
	select {
	case a.events <- e:
		return true
	case <-a.life.Done():
		return false
	}
}

// Native tools do not use the Core work-tool request map. Their explicit outcome
// is required before a failed turn can authorize replacement of its worker.
func (a *Adapter) observeNativeTool(id string, status *wire.ToolCallStatus, initial bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == "" {
		a.nativeOverflow = true
		return
	}
	if a.nativeTools == nil {
		a.nativeTools = map[string]bool{}
	}
	known, exists := a.nativeTools[id]
	if !exists && len(a.nativeTools) >= 1024 {
		a.nativeOverflow = true
		return
	}
	if initial {
		known = false
	}
	if status != nil {
		known = *status == wire.ToolCallStatusCompleted || *status == wire.ToolCallStatusFailed
	}
	a.nativeTools[id] = known
}
func (a *Adapter) effectsKnownLocked() bool {
	if len(a.pending) != 0 || a.nativeOverflow || a.uncertainEffects {
		return false
	}
	for _, known := range a.nativeTools {
		if !known {
			return false
		}
	}
	return true
}
