// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

type serverRequest struct {
	request    worker.Request
	thread     worker.Thread
	wireID     json.RawMessage
	key        string
	callID     string
	answered   bool
	resolved   bool
	turnScoped bool
}

// requestKey compares JSON-RPC IDs without floating-point conversion. A JSON
// string and integer with the same spelling are different identities.
func requestKey(id json.RawMessage) (string, error) {
	id = bytes.TrimSpace(id)
	if !json.Valid(id) || len(id) == 0 {
		return "", errors.New("invalid Codex request id")
	}
	if id[0] == '"' {
		var s string
		if err := json.Unmarshal(id, &s); err != nil {
			return "", err
		}
		return "s:" + s, nil
	}
	for i, b := range id {
		if b == '-' && i == 0 {
			continue
		}
		if b < '0' || b > '9' {
			return "", errors.New("Codex request id must be a string or integer")
		}
	}
	return "i:" + string(id), nil
}

func (s *Session) serverRequest(raw []byte, f frame) error {
	key, err := requestKey(f.ID)
	if err != nil {
		return err
	}
	var r wire.ServerRequest
	if err = json.Unmarshal(raw, &r); err != nil {
		return errors.New("invalid Codex server request")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event := worker.Event{}
	thread, turn, callID := "", "", ""
	switch {
	case r.ItemToolCall != nil:
		p := r.ItemToolCall.Params
		thread, turn, callID = p.ThreadId, p.TurnId, p.CallId
		namespace := ""
		if p.Namespace != nil {
			namespace = *p.Namespace
		}
		if namespace != "lina" || !s.tools[p.Tool] || callID == "" {
			return errors.New("undeclared Codex work tool")
		}
		event.Kind = worker.ToolCallEvent
		event.ToolCall = &worker.ToolCall{CallID: callID, Namespace: namespace, Name: p.Tool, Arguments: append(json.RawMessage(nil), p.Arguments...)}
	case r.ItemCommandExecutionRequestApproval != nil:
		p := r.ItemCommandExecutionRequestApproval.Params
		thread, turn = p.ThreadId, p.TurnId
		event.Kind = worker.ApprovalEvent
	case r.ItemFileChangeRequestApproval != nil:
		p := r.ItemFileChangeRequestApproval.Params
		thread, turn = p.ThreadId, p.TurnId
		event.Kind = worker.ApprovalEvent
	case r.ItemPermissionsRequestApproval != nil:
		p := r.ItemPermissionsRequestApproval.Params
		thread, turn = p.ThreadId, p.TurnId
		event.Kind = worker.ApprovalEvent
	case r.ItemToolRequestUserInput != nil:
		p := r.ItemToolRequestUserInput.Params
		thread, turn = p.ThreadId, p.TurnId
		event.Kind = worker.QuestionEvent
	case r.McpServerElicitationRequest != nil:
		p := r.McpServerElicitationRequest.Params
		thread = p.ThreadId
		if p.TurnId != nil {
			turn = *p.TurnId
		}
		event.Kind = worker.QuestionEvent
	default:
		return fmt.Errorf("unsupported Codex server request: %s", f.Method)
	}
	if s.failure != nil {
		return s.failure
	}
	if s.closing {
		return errors.New("Codex session closed")
	}
	if thread == "" || thread != s.snapshot.Thread.ID {
		return errors.New("foreign Codex server request")
	}
	// Elicitation may omit turnId. Bind it to the current turn at receipt time;
	// later turns cannot inherit the authority to answer it.
	if r.McpServerElicitationRequest != nil && r.McpServerElicitationRequest.Params.TurnId == nil {
		turn = s.snapshot.Thread.TurnID
	}
	turnScoped := r.McpServerElicitationRequest == nil || r.McpServerElicitationRequest.Params.TurnId != nil
	if turnScoped && (turn == "" || s.finished[turn]) {
		return errors.New("stale Codex server request")
	}
	if s.startingTurn && (s.snapshot.Thread.TurnID == "" || s.finished[s.snapshot.Thread.TurnID]) {
		s.snapshot.Thread.TurnID = turn
	}
	if turn != s.snapshot.Thread.TurnID {
		return errors.New("foreign Codex request turn")
	}
	if s.requests == nil {
		s.requests = map[string]*serverRequest{}
	}
	for _, pending := range s.requests {
		if pending.key == key || callID != "" && pending.callID == callID {
			return errors.New("duplicate Codex server request")
		}
	}
	// Bound both unanswered requests and answered tombstones until the turn ends.
	if len(s.requests) >= 256 {
		return errors.New("too many Codex server requests in one turn")
	}
	s.nextRequest++
	id := fmt.Sprintf("codex:%d:%d", s.generation, s.nextRequest)
	req := worker.Request{ID: id, Method: f.Method, Params: append(json.RawMessage(nil), f.Params...)}
	event.Thread = s.snapshot.Thread
	switch event.Kind {
	case worker.ToolCallEvent:
		event.ToolCall.ID = id
	case worker.QuestionEvent:
		event.Question = &req
	case worker.ApprovalEvent:
		event.Approval = &req
	case worker.ProgressEvent, worker.UsageEvent, worker.FinishEvent:
		return errors.New("invalid Codex request event")
	}
	retained := req
	retained.Params = append(json.RawMessage(nil), req.Params...)
	if err := s.publish(event); err != nil {
		return err
	}
	// Event consumers serialize with mu before answering; their payload does not
	// alias the retained body used to validate the response.
	s.requests[id] = &serverRequest{request: retained, thread: event.Thread, wireID: append(json.RawMessage(nil), f.ID...), key: key, callID: callID, turnScoped: turnScoped}
	s.revision++
	return nil
}

func (s *Session) pendingRequests() []worker.Request {
	var result []worker.Request
	for _, r := range s.requests {
		if !r.answered && r.thread == s.snapshot.Thread {
			req := r.request
			req.Params = append(json.RawMessage(nil), req.Params...)
			result = append(result, req)
		}
	}
	return result
}
func (s *Session) clearRequests(turn string) {
	for id, r := range s.requests {
		if turn == "" || r.thread.TurnID == turn {
			delete(s.requests, id)
		}
	}
}
func (s *Session) resolved(p wire.V2ServerRequestResolvedNotification) error {
	if p.ThreadId != s.snapshot.Thread.ID {
		return nil
	}
	key, err := requestKey(p.RequestId)
	if err != nil {
		return err
	}
	for _, r := range s.requests {
		if r.key == key && r.thread == s.snapshot.Thread {
			if r.resolved {
				return nil
			}
			r.answered = true
			r.resolved = true
			s.revision++
			return s.publish(worker.Event{Thread: r.thread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "server_request_resolved", ItemID: r.request.ID}})
		}
	}
	return nil
}

func (s *Session) Answer(ctx context.Context, t worker.Thread, a worker.Answer) error {
	if err := s.control.Lock(ctx); err != nil {
		return err
	}
	defer s.control.Unlock()
	s.mu.Lock()
	if err := s.bound(t, true); err != nil {
		s.mu.Unlock()
		return err
	}
	r := s.requests[a.RequestID]
	if r == nil || r.answered || r.thread != t || r.turnScoped && s.finished[t.TurnID] || s.closing {
		s.mu.Unlock()
		return errors.New("stale or unknown Codex answer")
	}
	if err := validateAnswer(r.request, a.Result); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	// Keep resolution/completion handling serialized with the actual response
	// write. A write failure has an unknown delivery outcome and fences the session.
	err := s.write(ctx, wire.JSONRPCResponse{ID: r.wireID, Result: a.Result})
	if err == nil {
		r.answered = true
		s.revision++
	}
	s.mu.Unlock()
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		s.fail(err)
	}
	return err
}
