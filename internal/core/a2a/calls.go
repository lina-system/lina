// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func (s *Service) Send(ctx context.Context, o Origin, id string, in Input) (Call, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	c, p, err := s.Validate(ctx, o, in)
	if err != nil {
		return Call{}, err
	}
	if id == "" {
		return Call{}, ErrInvalid
	}
	if err = s.unchanged(ctx, o, c, p); err != nil {
		return Call{}, err
	}
	if in.Stream && !c.Card.Capabilities.Streaming {
		return Call{}, ErrInvalid
	}
	if s.hasSecret(c, []byte(in.Text)) {
		return Call{}, ErrScope
	}
	if err = s.authenticate(c, http.Header{}); err != nil {
		return Call{}, err
	}
	call := Call{Admitted: c, ID: id, Origin: o, Connection: c.ID, Revision: c.Revision, Profile: p, MessageID: id, Deadline: time.Now().Add(10 * time.Minute), State: "unknown", Parent: in.Call}
	message := Message{ID: id, Role: "ROLE_USER", Parts: []Part{{Text: &in.Text, MediaType: "text/plain"}}}
	if in.Call != "" {
		previous, e := s.Call(ctx, o, in.Call)
		if e != nil {
			return Call{}, e
		}
		if previous.Connection != c.ID || previous.Revision != c.Revision || previous.State != "input_required" || previous.Answer != in.Text || previous.AnswerEpoch != o.Epoch || previous.Profile != p || previous.TaskID == "" || !time.Now().Before(previous.Deadline) {
			return Call{}, ErrScope
		}
		call.Deadline = previous.Deadline
		call.TaskID, call.ContextID = previous.TaskID, previous.ContextID
		message.Task, message.Context = previous.TaskID, previous.ContextID
	}
	call.Payload, _ = json.Marshal(map[string]any{"tenant": c.Interface.Tenant, "message": message, "configuration": map[string]any{"returnImmediately": true, "historyLength": 0, "acceptedOutputModes": []string{p.MediaType}}})
	call.PayloadHash = hash(call.Payload)
	call.Intent = hash([]byte(in.Text))
	if old, e := s.Call(ctx, o, id); e == nil {
		if old.PayloadHash != call.PayloadHash || old.Connection != c.ID || old.Revision != c.Revision || old.Profile != call.Profile {
			return Call{}, ErrInvalid
		}
		return old, nil
	}
	// A unique durable claim precedes the only possible send. Transport failure,
	// crash, unknown remote ids, and changed epochs never remove this claim.
	err = s.DB.Write(ctx, func(tx *canon.Tx) error {
		if err := s.epoch(ctx, tx); err != nil {
			return err
		}
		if in.Call == "" {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM a2a_calls WHERE person=? AND conversation=? AND json_extract(record,'$.state') IN ('unknown','working','input_required','auth_required','cancellation_unknown') AND (json_extract(record,'$.intent')=? OR (json_extract(record,'$.origin.turn')=? AND json_extract(record,'$.state') IN ('unknown','cancellation_unknown')))`, o.Person, o.Conversation, call.Intent, o.Turn).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return ErrScope
			}
		}
		if in.Call != "" {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM a2a_calls WHERE json_extract(record,'$.parent')=?`, in.Call).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return ErrScope
			}
		}
		raw, _ := json.Marshal(call)
		_, err := tx.ExecContext(ctx, "INSERT INTO a2a_calls(id,person,agent,conversation,connection,record) VALUES(?,?,?,?,?,?)", id, o.Person, o.Agent, o.Conversation, c.ID, raw)
		return err
	})
	if err != nil {
		return Call{}, err
	}
	method := "SendMessage"
	if in.Stream {
		method = "SendStreamingMessage"
	}
	err = s.rpc(ctx, c, &call, method, call.Payload, in.Stream)
	// Read-only recovery after stream loss. A server may have accepted a send
	// before any reply; absence of an id deliberately prevents reconciliation.
	if in.Stream && call.TaskID != "" && (call.State == "working" || call.State == "unknown") {
		err = s.poll(ctx, c, &call)
	}
	if err != nil {
		if errors.Is(err, ErrSignIn) {
			call.State = "auth_required"
			call.AuthVersion = c.CredentialVersion
		} else if call.State == "unknown" || call.TaskID == "" {
			call.State = "unknown"
		}
	}
	// Preserve the admission with a short independent write after cancellation.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if e := s.save(saveCtx, call); e != nil {
		return call, e
	}
	return call, err
}
func (s *Service) poll(ctx context.Context, c Connection, call *Call) error {
	params, _ := json.Marshal(map[string]any{"tenant": c.Interface.Tenant, "id": call.TaskID, "historyLength": 0})
	return s.rpc(ctx, c, call, "GetTask", params, false)
}
func (s *Service) Observe(ctx context.Context, o Origin, id string, stream, cancelRemote bool) (Call, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	call, err := s.Call(ctx, o, id)
	if err != nil {
		return call, err
	}
	c, err := s.Connection(ctx, o.Person, call.Connection)
	if err != nil {
		return call, err
	}
	if c.Revision != call.Revision {
		return call, ErrChanged
	}
	// A new authenticated conversation turn provides fresh observation authority.
	observed := call
	ctx = context.WithValue(ctx, observationAuthority{}, o)
	if err = s.unchanged(ctx, o, c, call.Profile); err != nil {
		return call, err
	}
	if call.TaskID == "" {
		return call, nil
	}
	if cancelRemote {
		call.State = "cancellation_unknown"
		call.Verified = false
		if err = s.save(ctx, call); err != nil {
			return call, err
		}
		params, _ := json.Marshal(map[string]any{"tenant": c.Interface.Tenant, "id": call.TaskID})
		err = s.rpc(ctx, c, &observed, "CancelTask", params, false)
		if err == nil && observed.RemoteState == "TASK_STATE_CANCELED" {
			observed.State = "canceled"
			observed.Verified = false
		} else {
			observed.State = "cancellation_unknown"
			observed.Verified = false
		}
	} else if stream && c.Card.Capabilities.Streaming {
		params, _ := json.Marshal(map[string]any{"tenant": c.Interface.Tenant, "id": call.TaskID})
		err = s.rpc(ctx, c, &observed, "SubscribeToTask", params, true)
		if err != nil || observed.State == "working" {
			err = s.poll(ctx, c, &observed)
		}
	} else {
		err = s.poll(ctx, c, &observed)
	}
	// Every stream checkpoint retained the original delivery origin.
	if e := s.save(context.WithoutCancel(ctx), observed); e != nil {
		return observed, e
	}
	return observed, err
}
func (s *Service) Answer(ctx context.Context, o Origin, id, text string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	call, err := s.Call(ctx, o, id)
	if err != nil {
		return err
	}
	if call.State != "input_required" || (call.Answer != "" && call.AnswerEpoch == o.Epoch) || len(text) == 0 || len(text) > call.Profile.MaxInput {
		return ErrScope
	}
	c, err := s.Connection(ctx, o.Person, call.Connection)
	if err != nil {
		return err
	}
	if !c.Enabled || c.Revision != call.Revision {
		return ErrScope
	}
	if s.hasSecret(c, []byte(text)) {
		return ErrScope
	}
	call.Answer = text
	call.AnswerEpoch = o.Epoch
	return s.save(ctx, call)
}
func (s *Service) observeTask(ctx context.Context, c Connection, call *Call, t Task) error {
	if s.reflectsCredential(c, []byte(t.ID)) || s.reflectsCredential(c, []byte(t.Context)) || t.ID == "" || t.Context == "" || len(t.ID) > 1024 || len(t.Context) > 1024 || (call.TaskID != "" && (call.TaskID != t.ID || call.ContextID != t.Context)) {
		return ErrUnknown
	}
	switch call.RemoteState {
	case "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_REJECTED", "TASK_STATE_CANCELED":
		if t.Status.State != call.RemoteState {
			return ErrUnknown
		}
	}
	call.TaskID, call.ContextID, call.RemoteState = t.ID, t.Context, t.Status.State
	// Persist the remote identity before artifact reads or later stream frames.
	if err := s.save(ctx, *call); err != nil {
		return err
	}
	switch t.Status.State {
	case "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING":
		call.State = "working"
	case "TASK_STATE_INPUT_REQUIRED":
		call.State = "input_required"
		call.Question = "The remote agent needs input."
	case "TASK_STATE_AUTH_REQUIRED":
		call.State = "auth_required"
		call.AuthVersion = c.CredentialVersion
		call.Question = "Sign in to the remote agent."
	case "TASK_STATE_FAILED", "TASK_STATE_REJECTED":
		call.State = "failed"
	case "TASK_STATE_CANCELED":
		call.State = "canceled"
	case "TASK_STATE_COMPLETED":
		return s.verify(ctx, c, call, t.Artifacts)
	default:
		return ErrUnknown
	}
	// Never retain credential-bearing or arbitrary remote instruction text as an
	// owner decision. The owner can inspect bounded, sanitized task questions.
	if call.State == "input_required" && t.Status.Message != nil {
		for _, part := range t.Status.Message.Parts {
			if part.Text != nil && len(*part.Text) <= 4096 && !s.hasSecret(c, []byte(*part.Text)) {
				call.Question = *part.Text
				break
			}
		}
	}
	return s.save(ctx, *call)
}
