// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
			if len(permissions) > 0 || len(requests) > 0 {
				a.mu.Lock()
				a.effectSeen = true
				a.mu.Unlock()
			}
			for len(permissions) > 0 {
				<-permissions
			}
			for len(requests) > 0 {
				<-requests
			}
			if result.Err != nil {
				if transportErr := c.Err(); transportErr != nil {
					// Join the owned process and descendants before admitting a
					// terminal snapshot. A timeout waiting for a reply is not death.
					c.Close()
					a.mu.Lock()
					// Malformed or overflowed streams may have lost effect evidence.
					a.transportEnded = errors.Is(transportErr, io.EOF) || errors.Is(transportErr, io.ErrClosedPipe)
					a.mu.Unlock()
				}
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
	a.mu.Lock()
	if v.ToolCall != nil || v.ToolCallUpdate != nil || len(v.Unrecognized) > 0 {
		a.effectSeen = true
	}
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
	case v.SessionInfoUpdate != nil:
		p.Phase = "doing"
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
	a.terminal = &f
	a.effectSeen = a.effectSeen || len(a.requests) > 0 || len(a.pending) > 0
	a.requests = nil
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
