// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"encoding/json"
	"errors"
)

// Request retains the exact incoming id and params. Answer is single-use even
// when it races cancellation. Callers cannot replace the stored request id.
type Request struct {
	ID         RequestID
	Method     string
	Params     json.RawMessage
	client     *Client
	key        string
	id         json.RawMessage
	session    SessionID
	permission bool
}

// Permission is a typed approval request; Answer selects one offered option.
// The caller's approval policy decides which options may be selected.
type Permission struct {
	Request RequestPermissionRequest
	request *Request
}

func (p *Permission) Answer(ctx context.Context, option PermissionOptionID) error {
	return p.AnswerWithMeta(ctx, option, nil)
}

// AnswerWithMeta carries protocol extension metadata with the selected option.
func (p *Permission) AnswerWithMeta(ctx context.Context, option PermissionOptionID, meta *map[string]json.RawMessage) error {
	for _, o := range p.Request.Options {
		if o.OptionID == option {
			return p.request.Answer(ctx, RequestPermissionResponse{Meta: meta, Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionID: option}}})
		}
	}
	return errors.New("ACP permission option was not offered")
}
func (r *Request) Answer(ctx context.Context, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	c := r.client
	c.mu.Lock()
	if c.requests[r.key] != r {
		c.mu.Unlock()
		return ErrAnswered
	}
	delete(c.requests, r.key)
	c.mu.Unlock()
	return c.write(ctx, envelope{ID: r.id, Result: raw})
}
func (c *Client) request(f envelope) error {
	key, err := idKey(f.ID)
	if err != nil {
		return err
	}
	if f.Method != "session/request_permission" && f.Method != "elicitation/create" {
		return c.write(c.ctx, envelope{ID: f.ID, Error: &Error{Code: -32601, Message: "Method not found"}})
	}
	r := &Request{ID: append(RequestID(nil), f.ID...), Method: f.Method, Params: append(json.RawMessage(nil), f.Params...), client: c, key: key, id: append(json.RawMessage(nil), f.ID...)}
	var permission RequestPermissionRequest
	if f.Method == "session/request_permission" {
		if err = json.Unmarshal(f.Params, &permission); err != nil {
			return err
		}
		if permission.SessionID == "" || permission.ToolCall.ToolCallID == "" || len(permission.Options) == 0 {
			return errors.New("ACP malformed permission request")
		}
		r.session = permission.SessionID
		r.permission = true
	} else {
		var p struct {
			Mode      string    `json:"mode"`
			SessionID SessionID `json:"sessionId"`
		}
		if err = json.Unmarshal(f.Params, &p); err != nil {
			return err
		}
		if p.Mode != "form" {
			return c.write(c.ctx, envelope{ID: f.ID, Error: &Error{Code: -32602, Message: "Only form input is supported"}})
		}
		r.session = p.SessionID
	}
	c.mu.Lock()
	if c.requests[key] != nil {
		c.mu.Unlock()
		return errors.New("ACP duplicate pending request id")
	}
	if len(c.requests) >= 256 {
		c.mu.Unlock()
		return ErrOverflow
	}
	c.requests[key] = r
	s := c.sessions[r.session]
	cancelled := s != nil && s.cancelled
	c.mu.Unlock()
	if cancelled {
		return r.Answer(c.ctx, cancelledResult(r.permission))
	}
	if r.permission {
		select {
		case c.permissions <- &Permission{permission, r}:
			return nil
		default:
			return ErrOverflow
		}
	}
	select {
	case c.incoming <- r:
		return nil
	default:
		return ErrOverflow
	}
}
func cancelledResult(permission bool) any {
	if permission {
		return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Cancelled: &RequestPermissionOutcomeCancelled{}}}
	}
	return CreateElicitationResponse{Cancel: &CreateElicitationResponseCancel{}}
}

// Cancel answers all pending permissions for this session, then sends the
// session/cancel notification. The prompt's eventual stop reason ends the turn.
func (c *Client) Cancel(ctx context.Context, session SessionID) error {
	c.control.Lock()
	defer c.control.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if session == "" {
		return errors.New("ACP cancel requires a session")
	}
	c.mu.Lock()
	if c.failure != nil {
		err := c.failure
		c.mu.Unlock()
		return err
	}
	if !c.ready {
		c.mu.Unlock()
		return ErrNotInitialized
	}
	s := c.sessions[session]
	if s == nil {
		s = &sessionState{}
		c.sessions[session] = s
	}
	s.cancelled = true
	var requests []*Request
	for key, r := range c.requests {
		if r.session == session {
			requests = append(requests, r)
			delete(c.requests, key)
		}
	}
	c.mu.Unlock()
	for _, r := range requests {
		raw, err := json.Marshal(cancelledResult(r.permission))
		if err != nil {
			return err
		}
		if err = c.write(ctx, envelope{ID: r.id, Result: raw}); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(CancelNotification{SessionID: session})
	if err != nil {
		return err
	}
	return c.write(ctx, envelope{Method: "session/cancel", Params: raw})
}
