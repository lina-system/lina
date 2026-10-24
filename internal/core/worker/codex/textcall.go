// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"fmt"

	"github.com/thisisjun786/lina/internal/core/textcall"
	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

// Assistant text stays private until the turn is classified. Complete items
// replace their deltas; items in the terminal notification cover early finishes.
func (s *Session) textBinding(thread, turn string) bool {
	if thread != s.snapshot.Thread.ID || turn == "" || s.finished[turn] || s.finishEvents[turn] {
		return false
	}
	if s.startingTurn && (s.snapshot.Thread.TurnID == "" || s.finished[s.snapshot.Thread.TurnID]) {
		s.snapshot.Thread.TurnID = turn
	}
	return turn == s.snapshot.Thread.TurnID
}
func (s *Session) textItem(item wire.V2ThreadItem) {
	switch {
	case item.AgentMessage != nil:
		s.text.Add(item.AgentMessage.ID, item.AgentMessage.Text, true)
	case item.UserMessage != nil, item.Plan != nil, item.Reasoning != nil, item.ContextCompaction != nil:
		// Non-executing items cannot establish a real tool call.
	default:
		s.realTool = true
	}
}
func (s *Session) textSnapshot() worker.ThreadSnapshot {
	result := s.snapshot
	result.Pending = s.pendingRequests()
	if len(result.Pending) > 0 {
		result.State = worker.Waiting
		result.EffectsKnown = false
	}
	if result.Finish != nil {
		f := *result.Finish
		result.Finish = &f
	}
	return result
}
func (s *Session) finishText(f *worker.Finish) (bool, error) {
	if s.text.Overflow {
		s.textHandled = true
		return true, s.completeText(worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "assistant_text_limit"})
	}
	text := s.text.Text()
	reason := ""
	if f.Status == worker.Done {
		reason = "end_turn"
	}
	decision := worker.DetectTextCall(text, reason, s.realTool, s.toolDeclarations)
	s.text = worker.TurnText{}
	if !decision.Detected {
		s.snapshot.Output = text
		if text != "" {
			return false, s.publish(worker.Event{Thread: s.snapshot.Thread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "item_completed", Text: text}})
		}
		return false, nil
	}
	s.textHandled = true
	s.snapshot.Output = ""
	s.clearRequests(s.snapshot.Thread.TurnID)
	if decision.Action == textcall.Restart {
		return true, s.completeText(worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: worker.TextCallFailure})
	}
	s.nextRequest++
	id := fmt.Sprintf("codex:%d:%d", s.generation, s.nextRequest)
	req := worker.Request{ID: id, Method: "item/tool/call"}
	if s.requests == nil {
		s.requests = map[string]*serverRequest{}
	}
	s.requests[id] = &serverRequest{request: req, thread: s.snapshot.Thread, callID: id, turnScoped: true, textFinish: &worker.Finish{Status: worker.Done, Code: "text_tool_call_recovered"}}
	s.snapshot.State = worker.Waiting
	s.revision++
	return true, s.publish(worker.Event{Thread: s.snapshot.Thread, Kind: worker.ToolCallEvent, ToolCall: &worker.ToolCall{ID: id, CallID: id, Namespace: "lina", Name: decision.Call.Name, Arguments: decision.Call.Arguments}})
}
func (s *Session) completeText(f worker.Finish) error {
	s.finished[s.snapshot.Thread.TurnID] = true
	s.snapshot.State = worker.Idle
	s.snapshot.Finish = &f
	s.snapshot.EffectsKnown = f.Code == worker.TextCallFailure
	s.revision++
	return s.publish(worker.Event{Thread: s.snapshot.Thread, Kind: worker.FinishEvent, Finish: &f})
}
