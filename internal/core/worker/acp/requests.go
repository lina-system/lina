// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
)

type pendingDecision struct {
	request    worker.Request
	permission *wire.Permission
	question   *wire.Request
}

func (a *Adapter) permission(th worker.Thread, p *wire.Permission) bool {
	if string(p.Request.SessionID) != th.ID {
		a.finish(th, worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "foreign_request"})
		return false
	}
	raw, err := json.Marshal(p.Request)
	if err != nil {
		return false
	}
	return a.request(th, &pendingDecision{request: worker.Request{Method: "session/request_permission", Params: raw}, permission: p})
}
func (a *Adapter) question(th worker.Thread, r *wire.Request) bool {
	var scope struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(r.Params, &scope) != nil || scope.SessionID != th.ID {
		_ = r.Answer(a.life, wire.CreateElicitationResponse{Cancel: &wire.CreateElicitationResponseCancel{}})
		return true
	}
	if _, err := ParseForm(r.Params); err != nil {
		_ = r.Answer(a.life, wire.CreateElicitationResponse{Decline: &wire.CreateElicitationResponseDecline{}})
		return true
	}
	return a.request(th, &pendingDecision{request: worker.Request{Method: r.Method, Params: append(json.RawMessage(nil), r.Params...)}, question: r})
}
func (a *Adapter) request(th worker.Thread, p *pendingDecision) bool {
	a.mu.Lock()
	if !a.matches(th) || a.cancelled {
		a.mu.Unlock()
		return true
	}
	a.nextRequest++
	p.request.ID = fmt.Sprintf("acp:%d:%d:%d", th.BindingEpoch, th.ProcessGeneration, a.nextRequest)
	if a.requests == nil {
		a.requests = map[string]*pendingDecision{}
	}
	a.requests[p.request.ID] = p
	a.state = worker.Waiting
	r := p.request
	r.Params = append(json.RawMessage(nil), r.Params...)
	a.mu.Unlock()
	e := worker.Event{Thread: th}
	if p.permission != nil {
		e.Kind = worker.ApprovalEvent
		e.Approval = &r
	} else {
		e.Kind = worker.QuestionEvent
		e.Question = &r
	}
	return a.emit(e)
}
func (a *Adapter) pendingLocked() []worker.Request {
	pending := make([]worker.Request, 0, len(a.requests))
	for _, p := range a.requests {
		r := p.request
		r.Params = append(json.RawMessage(nil), r.Params...)
		pending = append(pending, r)
	}
	return pending
}

// Answer serializes with Interrupt and Close. Invalid answers leave the handle
// available; a transport error consumes it because delivery may have succeeded.
func (a *Adapter) Answer(ctx context.Context, th worker.Thread, answer worker.Answer) error {
	a.control.Lock()
	defer a.control.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.matches(th) {
		return ErrThread
	}
	p := a.requests[answer.RequestID]
	if p == nil || a.cancelled {
		return errors.New("stale ACP answer")
	}
	var send func() error
	if p.permission != nil {
		var response wire.RequestPermissionResponse
		if json.Unmarshal(answer.Result, &response) != nil || response.Outcome.Selected == nil && response.Outcome.Cancelled == nil {
			return errors.New("invalid ACP approval answer")
		}
		if response.Outcome.Cancelled != nil {
			a.cancelled = true
			a.requests = nil
			a.state = worker.Active
			return a.client.Cancel(ctx, wire.SessionID(th.ID))
		}
		option := response.Outcome.Selected.OptionID
		count := 0
		for _, o := range p.permission.Request.Options {
			if o.OptionID == option {
				if o.Kind != wire.PermissionOptionKindAllowOnce && o.Kind != wire.PermissionOptionKindRejectOnce {
					return errors.New("persistent ACP permission is unsupported")
				}
				count++
			}
		}
		if count != 1 {
			return errors.New("ACP permission option is not unique or was not offered")
		}
		send = func() error { return p.permission.Answer(ctx, option) }
	} else {
		var response wire.CreateElicitationResponse
		if json.Unmarshal(answer.Result, &response) != nil || response.Accept == nil && response.Decline == nil && response.Cancel == nil {
			return errors.New("invalid ACP form answer")
		}
		if response.Accept != nil {
			form, err := ParseForm(p.request.Params)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(response.Accept.Content)
			if err != nil {
				return err
			}
			if err = form.validate(raw); err != nil {
				return err
			}
		}
		send = func() error { return p.question.Answer(ctx, answer.Result) }
	}
	delete(a.requests, answer.RequestID)
	if len(a.requests) == 0 {
		a.state = worker.Active
	}
	return send()
}

// PermissionReply selects only the requested once kind, independent of option
// order or labels. With no unique safe option, cancel instead of granting a
// remembered permission or leaving an answered card waiting forever.
func PermissionReply(params json.RawMessage, allow bool) (json.RawMessage, error) {
	var request wire.RequestPermissionRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, err
	}
	kind := wire.PermissionOptionKindRejectOnce
	if allow {
		kind = wire.PermissionOptionKindAllowOnce
	}
	var option wire.PermissionOptionID
	count := 0
	for _, o := range request.Options {
		if o.Kind == kind {
			option = o.OptionID
			count++
		}
	}
	unique := 0
	for _, o := range request.Options {
		if o.OptionID == option {
			unique++
		}
	}
	response := wire.RequestPermissionResponse{Outcome: wire.RequestPermissionOutcome{Cancelled: &wire.RequestPermissionOutcomeCancelled{}}}
	if count == 1 && unique == 1 && option != "" {
		response.Outcome = wire.RequestPermissionOutcome{Selected: &wire.RequestPermissionOutcomeSelected{OptionID: option}}
	}
	return json.Marshal(response)
}
