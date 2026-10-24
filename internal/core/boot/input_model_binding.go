// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

const modelBindingKind = "input_binding_choice"

type bindingBatch struct {
	Bindings []binding.Choice `json:"bindings"`
}
type bindingRequest struct {
	InputEventID string           `json:"input_event_id,omitempty"`
	Input        string           `json:"input,omitempty"`
	Bindings     []binding.Choice `json:"bindings"`
}

type rawBindingRequest struct {
	InputEventID string        `json:"input_event_id,omitempty"`
	Input        string        `json:"input,omitempty"`
	Text         string        `json:"text,omitempty"`
	Bindings     rawChoiceList `json:"bindings"`
}

// A batch is normally an array. Accept one object too so a model that binds a
// single request does not lose the whole call to a shape error.
type rawChoiceList []json.RawMessage

func (r *rawChoiceList) UnmarshalJSON(raw []byte) error {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		*r = list
		return nil
	}
	var one json.RawMessage
	if json.Unmarshal(raw, &one) != nil {
		return binding.ErrChoice
	}
	*r = []json.RawMessage{one}
	return nil
}

// decodeBindingRequest accepts the natural argument shapes and ignores
// unknown metadata instead of failing the call.
func decodeBindingRequest(raw string) (rawBindingRequest, error) {
	var in rawBindingRequest
	if len(raw) > 32<<10 || json.Unmarshal([]byte(raw), &in) != nil {
		return in, binding.ErrChoice
	}
	if len(in.Bindings) == 0 || len(in.Bindings) > 32 {
		return in, binding.ErrChoice
	}
	return in, nil
}

func readInputChoices(ctx context.Context, tx *canon.Tx, input ledger.Event) ([]binding.Choice, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT body FROM conv_event WHERE id=?`, inputBindingID(input.ID)).Scan(&raw); err != nil {
		return nil, err
	}
	var initial binding.Decision
	if err := json.Unmarshal([]byte(raw), &initial); err != nil {
		return nil, err
	}
	choices := []binding.Choice{{Request: 0, Quote: input.Body.Text(), Target: initial.Target, Decider: initial.Decider}}
	rows, err := tx.QueryContext(ctx, `SELECT body FROM conv_event WHERE parent_id=? AND scaffold_kind=? ORDER BY rowid`, input.ID, modelBindingKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var batch bindingBatch
		if err = json.Unmarshal([]byte(raw), &batch); err != nil {
			return nil, err
		}
		for _, c := range batch.Bindings {
			if c.Request > len(choices) || c.Request < 0 {
				return nil, binding.ErrChoice
			}
			if c.Request == len(choices) {
				choices = append(choices, c)
			} else {
				choices[c.Request] = c
			}
		}
	}
	return choices, rows.Err()
}
func appendInputChoices(ctx context.Context, tx *canon.Tx, input ledger.Event, id string, choices []binding.Choice) error {
	raw, err := json.Marshal(bindingBatch{choices})
	if err != nil {
		return err
	}
	masked, err := secrets.Mask(string(raw))
	if err != nil {
		return err
	}
	var old, parent, kind string
	err = tx.QueryRowContext(ctx, "SELECT body,parent_id,scaffold_kind FROM conv_event WHERE id=?", id).Scan(&old, &parent, &kind)
	if err == nil {
		if old == masked.Text() && parent == input.ID && kind == modelBindingKind {
			return nil
		}
		return binding.ErrChoice
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = ledger.AppendSystemSnapshot(ctx, tx, ledger.Input{ID: id, ParentID: input.ID, ConversationID: input.ConversationID, AgentID: input.AgentID, Surface: input.Surface, ScaffoldKind: modelBindingKind, OccurredAt: input.RecordedAt, RecordedAt: time.Now().UTC(), TimeQuality: "observed", Body: string(raw), Relations: []ledger.Relation{{TargetID: input.ID, Kind: "view_of"}}})
	return err
}
func (r *conversationRuntime) currentInputChoices(ctx context.Context, input ledger.Event) (out []binding.Choice, err error) {
	err = r.db.Read(ctx, func(tx *canon.Tx) error { out, err = readInputChoices(ctx, tx, input); return err })
	return
}

// The input identity is supplied by the live turn or its consumed steering,
// never by an arbitrary ledger id the model invents.
func (s *turnState) bindingInput(id string) (ledger.Event, *conversationRuntime, error) {
	id = strings.TrimSpace(id)
	switch strings.ToLower(id) {
	case "current", "current_input", "this", "this input", "same":
		return s.currentBindingInput()
	}
	if s.bindingSource != nil && (id == "" || sameBindingSelector(id, *s.bindingSource)) {
		return *s.bindingSource, s.runtime, nil
	}
	if id == "" || sameBindingSelector(id, s.input) {
		return s.input, s.runtime, nil
	}
	if s.bindingInputs != nil {
		if a, ok := s.bindingInputs[id]; ok {
			r := a.runner
			if r == nil {
				r = s.runtime
			}
			return a.event, r, nil
		}
		for _, a := range s.bindingInputs {
			if sameBindingSelector(id, a.event) {
				r := a.runner
				if r == nil {
					r = s.runtime
				}
				return a.event, r, nil
			}
		}
	}
	// A model that puts the message itself in the selector field names the one
	// input this turn has. Event-like selectors stay strict: an admitted event
	// the turn did not consume is not a binding target.
	if !bindingSelectorIsEventID(id) && s.bindingSource == nil && len(s.bindingInputs) == 0 {
		return s.input, s.runtime, nil
	}
	return ledger.Event{}, nil, &binding.ChoiceError{Field: "input_event_id", Reason: "unknown_input_event"}
}

func (s *turnState) currentBindingInput() (ledger.Event, *conversationRuntime, error) {
	if s.bindingSource != nil {
		return *s.bindingSource, s.runtime, nil
	}
	return s.input, s.runtime, nil
}

// sameBindingSelector compares an event id or the input text.
func sameBindingSelector(selector string, event ledger.Event) bool {
	if selector == event.ID {
		return true
	}
	normalized := func(value string) string {
		return binding.Fold(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	}
	return selector != "" && normalized(selector) == normalized(event.Body.Text())
}

func bindingSelectorIsEventID(id string) bool {
	return id != "" && !strings.ContainsAny(id, " \t\r\n") && len(id) <= 256
}

// Selectors may appear under more than one argument name. They must all
// resolve to the same admitted event if a caller supplies several.
func (s *turnState) selectedBindingInput(id, legacy string) (ledger.Event, *conversationRuntime, error) {
	return s.selectedBindingInputValues(id, legacy)
}

func (s *turnState) selectedBindingInputValues(values ...string) (ledger.Event, *conversationRuntime, error) {
	var input ledger.Event
	var runner *conversationRuntime
	chosen := false
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		event, r, err := s.bindingInput(value)
		if err != nil {
			return event, r, err
		}
		if chosen && event.ID != input.ID {
			return ledger.Event{}, nil, &binding.ChoiceError{Field: "input_event_id", Reason: "conflicting_input_selectors"}
		}
		input, runner, chosen = event, r, true
	}
	if !chosen {
		return s.bindingInput("")
	}
	return input, runner, nil
}

func inputEventIDSchema() map[string]string {
	return map[string]string{"type": "string", "description": "Consumed input event ID; omit for current."}
}
func (s *turnState) prepareInputBinding(declarations []provider.ToolDecl) []provider.ToolDecl {
	if s.input.SpeakerKind != guard.Person {
		if s.input.ScaffoldKind != "question_answer" {
			return declarations
		}
		qid := strings.TrimSuffix(s.input.ID, ":answer")
		q, err := waiting.NewConversationQuestions(s.runtime.db).Get(s.runtime.ctx, qid)
		if err != nil || q.Binding == nil {
			return declarations
		}
		original, err := s.runtime.journal().Get(s.runtime.ctx, q.Input)
		if err != nil {
			return declarations
		}
		s.bindingSource = &original
		s.bindingRequest = q.Binding.Request
	}
	s.bindings["bind_input"] = loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			_, err := decodeBindingRequest(c.Arguments)
			return "input-binding:" + s.input.ID, err
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			in, err := decodeBindingRequest(c.Arguments)
			if err != nil {
				return bindingFailure(c.ID, err)
			}
			input, r, err := s.selectedBindingInputValues(in.InputEventID, in.Input, in.Text)
			if err != nil {
				return bindingFailure(c.ID, err)
			}
			choices, err := r.saveModelRawBinding(ctx, input, "binding-choice:"+s.turnID+":"+c.ID, in.Bindings, "model")
			if err != nil {
				return bindingFailure(c.ID, err)
			}
			for _, choice := range choices {
				if choice.Question != "" {
					if err = r.questionDecisions().PublishQuestion(ctx, choice.Question); err != nil {
						return bindingFailure(c.ID, err)
					}
				}
			}
			raw, _ := json.Marshal(bindingBatch{choices})
			return loop.Result{ID: c.ID, Code: loop.OK, Output: raw}
		},
	}
	declarations = append(declarations, bindInputDeclaration())
	return s.wrapInputTools(declarations)
}
func bindingFailure(id string, err error) loop.Result {
	code := "input_binding_failed"
	result := loop.Failed
	if errors.Is(err, binding.ErrChoice) {
		code = "invalid_input_binding"
		result = loop.Invalid
	}
	if errors.Is(err, errBindingPending) {
		code = "input_target_pending"
		result = loop.Denied
	}
	body := map[string]any{"code": code}
	var detail *binding.ChoiceError
	if errors.As(err, &detail) {
		body["field"], body["reason"] = detail.Field, detail.Reason
		if detail.Allowed != nil {
			body["allowed"] = detail.Allowed
		}
		if detail.AllowedParentKinds != nil {
			body["allowed_parent_kinds"] = detail.AllowedParentKinds
			body["parent_optional"] = true
		}
	}
	raw, _ := json.Marshal(body)
	return loop.Result{ID: id, Code: result, Output: raw}
}

var errBindingPending = errors.New("input_target_pending")

// personTargetBlocked keeps a person's settled target. A selected new target
// may later be read or followed as the item creation recorded.
func personTargetBlocked(old binding.Choice, next binding.Target) bool {
	if old.Question != "" || old.Cancelled {
		return true
	}
	if old.Decider != "person" || old.Target == next {
		return false
	}
	return !createdIdentity(old, next)
}

func createdIdentity(old binding.Choice, next binding.Target) bool {
	if old.CreatedID == "" || next.Class != "existing" || next.ID != old.CreatedID || next.Parent != "" {
		return false
	}
	switch old.Target.Class {
	case "new":
		return next.Kind == old.Target.Kind
	case "existing":
		return old.Target.ID == next.ID && old.Target.Kind == next.Kind
	default:
		return false
	}
}

// saveModelBinding keeps the canonical call shape for in-process callers.
func (r *conversationRuntime) saveModelBinding(ctx context.Context, input ledger.Event, id string, choices []binding.Choice, decider string) ([]binding.Choice, error) {
	raws := make([]json.RawMessage, len(choices))
	for n, choice := range choices {
		raw, err := json.Marshal(choice)
		if err != nil {
			return nil, err
		}
		raws[n] = raw
	}
	return r.saveModelRawBinding(ctx, input, id, raws, decider)
}

// saveModelRawBinding normalizes the arguments a model sent before it stores or
// validates them. Normalization needs the candidate snapshot, so it runs inside
// the write transaction; a bad excerpt, kind or parent never aborts the call.
func (r *conversationRuntime) saveModelRawBinding(ctx context.Context, input ledger.Event, id string, raws []json.RawMessage, decider string) (out []binding.Choice, err error) {
	if input.PersonID != r.person || input.AgentID != r.agent || input.ConversationID != r.conversation || input.SpeakerKind != guard.Person || r.channelBinding != nil && (!r.channelBinding.Scope.Tools || r.validateChannel(ctx) != nil) {
		return nil, binding.ErrChoice
	}
	err = r.db.Write(ctx, func(tx *canon.Tx) error {
		state, e := r.bindingState(ctx, tx, input)
		if e != nil {
			return e
		}
		choices, e := binding.NormalizeChoices(state, input.Body.Text(), raws)
		if e != nil {
			return e
		}
		// An immutable call is idempotent, including its originally pending card.
		// Compare before request placement so a retry without an ordinal resolves
		// to the same stored request.
		var raw string
		e = tx.QueryRowContext(ctx, `SELECT body FROM conv_event WHERE id=? AND parent_id=? AND scaffold_kind=?`, id, input.ID, modelBindingKind).Scan(&raw)
		if e == nil {
			var batch bindingBatch
			if err := json.Unmarshal([]byte(raw), &batch); err != nil {
				return err
			}
			if len(batch.Bindings) != len(choices) {
				return binding.ErrChoice
			}
			for n, saved := range batch.Bindings {
				if !sameChoice(saved, choices[n], choices[n].Request < 0) {
					return binding.ErrChoice
				}
			}
			out = batch.Bindings
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		visible, e := waiting.ConversationQuestionVisible(ctx, tx, waiting.ConversationQuestion{Input: input.ID, Agent: r.agent})
		if e != nil {
			return e
		}
		if !visible {
			return binding.ErrChoice
		}
		old, e := readInputChoices(ctx, tx, input)
		if e != nil {
			return e
		}
		assignBindingRequests(choices, old)
		normalized, e := json.Marshal(choices)
		if e != nil {
			return e
		}
		masked, e := secrets.Mask(string(normalized))
		if e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(masked.Text()), &choices); e != nil {
			return e
		}
		seen := map[int]bool{}
		for n := range choices {
			c := &choices[n]
			if seen[c.Request] || c.Question != "" || c.Cancelled || c.CreatedID != "" || c.Decider != "" || len(c.Probabilities) > 0 {
				return binding.ErrChoice
			}
			seen[c.Request] = true
			if e = binding.ValidateChoice(state, input.Body.Text(), *c); e != nil {
				return e
			}
			if c.Request > len(old) {
				return binding.ErrChoice
			}
			if c.Request < len(old) && personTargetBlocked(old[c.Request], c.Target) {
				return errBindingPending
			}
			if c.Request == len(old) {
				old = append(old, *c)
			}
			c.Decider = decider
			if c.Request < len(old) && old[c.Request].Decider == "person" {
				c.Decider = "person"
				if createdIdentity(old[c.Request], c.Target) {
					c.CreatedID = old[c.Request].CreatedID
				}
			}
			if binding.NeedsQuestion(state, *c) {
				qid := fmt.Sprintf("%starget:%s:%d", conversationQuestionPrefix, input.ID, c.Request)
				labels := make([]string, len(c.Tied))
				for j, target := range c.Tied {
					labels[j] = bindingChoiceLabel(state, target)
				}
				draft := waiting.QuestionDraft{Question: "Which item should this apply to?", Recommendation: labels[0], Choices: labels, Default: labels[0]}
				q := waiting.ConversationQuestion{ID: qid, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: input.ID, Turn: "binding:" + input.ID, AskedAt: time.Now().UTC(), QuestionDraft: draft, Binding: c}
				if e = waiting.NewConversationQuestions(questionTransaction{tx}).Ask(ctx, q, 32); e != nil {
					return e
				}
				c.Question = qid
			}
		}
		if e = appendInputChoices(ctx, tx, input, id, choices); e != nil {
			return e
		}
		out = choices
		return nil
	})
	return
}

func bindingChoiceLabel(state binding.State, target binding.Target) string {
	if target.Class == "new" {
		label := "new " + target.Kind
		if target.Parent != "" {
			parent := target.Parent
			for _, item := range state.Items {
				if item.ID == target.Parent {
					parent = item.Title + " (" + item.ID + ")"
					break
				}
			}
			label += " under " + parent
		}
		return label
	}
	for _, item := range state.Items {
		if item.ID == target.ID {
			return item.Title + " (" + item.ID + ")"
		}
	}
	return target.Class
}

// assignBindingRequests places every omitted ordinal in message order. A single
// call without an ordinal replaces the admission default; once a request has a
// decided binding, a later call without an ordinal binds the next request.
func assignBindingRequests(choices []binding.Choice, old []binding.Choice) {
	used := map[int]bool{}
	for n := range choices {
		c := &choices[n]
		if c.Request < 0 && len(choices) == 1 {
			if len(old) > 0 && !admissionDecider(old[0].Decider) {
				c.Request = len(old)
			} else {
				c.Request = 0
			}
			continue
		}
		request := c.Request
		if request < 0 || request >= 32 || used[request] {
			request = -1
			for i := 0; i < 32; i++ {
				if !used[i] {
					request = i
					break
				}
			}
		}
		if request < 0 {
			request = n
		}
		used[request] = true
		c.Request = request
	}
}

// A current rule or the absence of one leaves request zero open to the model.
func admissionDecider(decider string) bool {
	switch decider {
	case "", "focus", "no-rule", "exact-reference", "reply-or-card", "offered-option":
		return true
	}
	return false
}

// sameChoice compares a retry with its stored batch. An omitted ordinal is
// placed later, so it is not part of the comparison.
func sameChoice(saved, incoming binding.Choice, ignoreRequest bool) bool {
	if !ignoreRequest && saved.Request != incoming.Request {
		return false
	}
	saved.Decider, incoming.Decider = "", ""
	saved.Question, incoming.Question = "", ""
	saved.Probabilities, incoming.Probabilities = nil, nil
	if ignoreRequest {
		saved.Request, incoming.Request = 0, 0
	}
	return reflect.DeepEqual(saved, incoming)
}

func bindInputDeclaration() provider.ToolDecl {
	target := map[string]any{"type": "object", "properties": map[string]any{"class": map[string]any{"type": "string", "enum": []string{"existing", "new", "none"}}, "id": map[string]string{"type": "string", "description": "For existing: the candidate ID. Omit for new or none."}, "kind": map[string]string{"type": "string", "description": "For existing: copy the candidate kind. For new: initiative, project, todo, task or run (todo when omitted)."}, "parent": map[string]string{"type": "string", "description": "For new only: optional ID of an open initiative or project."}}, "required": []string{"class"}, "additionalProperties": false}
	properties := map[string]any{"request": map[string]any{"type": "integer", "minimum": 0, "maximum": 31, "description": "Optional ordinal. Omit to place the binding in message order."}, "quote": map[string]string{"type": "string", "description": "Optional excerpt from the input; the input text is used when omitted."}, "target": target, "tied": map[string]any{"type": "array", "items": target, "minItems": 2, "maxItems": 5}, "action": map[string]any{"type": "string", "enum": []string{"read", "edit", "steer", "start", "send", "pay", "delete", "stop"}}, "close_matches": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}, "difference": map[string]string{"type": "string"}}
	raw, _ := json.Marshal(map[string]any{"type": "function", "name": "bind_input", "description": "Bind each request in the current input, in order from zero, to an authorized candidate, new (with kind and optional parent), or none. A target may be the candidate object, its exact ID or title, \"new\" or \"none\". An exact candidate ID is bound even when it starts with a kind name. A reply, card or thread reference settles the item before any name in the text; a name counts only as a whole word. A request to add something to an item is new work under it: bind new with that item as its parent. With no action or explicit choice, keep a fresh focus; otherwise none. A request to start something new binds new when no candidate matches. Split only when the input asks for several distinct things. Use tied only when two targets remain equally plausible after reading the input and context, recommended first, and name the intended action. Core holds irreversible ties for a target question; never execute a held request. Otherwise pick the most likely target without asking. For new work mark close existing matches and record why it differs, or bind to that existing item. This grants no authority. Omit input_event_id for the current input; it selects an admitted event only when binding consumed steering. Work-tool input_binding uses the same selector and defaults to request 0.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"input_event_id": inputEventIDSchema(), "bindings": map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": map[string]any{"type": "object", "properties": properties, "required": []string{"target"}, "additionalProperties": false}}}, "required": []string{"bindings"}, "additionalProperties": false}})
	return raw
}

// Explicit person answers close only the named request. No default applies.
func resolveTargetBinding(ctx context.Context, tx *canon.Tx, input ledger.Event, q waiting.ConversationQuestion, d waiting.QuestionResolution) error {
	if q.Binding == nil {
		return nil
	}
	c := *q.Binding
	c.Question = ""
	c.Decider = "person"
	c.Tied = nil
	if d.Kind == "answered" {
		index := slices.Index(q.Choices, d.Value)
		if index < 0 || index >= len(q.Binding.Tied) {
			return binding.ErrChoice
		}
		c.Target = q.Binding.Tied[index]
	} else if d.Kind == "cancelled" || d.Kind == "declined" {
		c.Cancelled = true
		c.Target = binding.Target{Class: "none"}
	} else {
		return errBindingPending
	}
	return appendInputChoices(ctx, tx, input, "binding-answer:"+q.ID, []binding.Choice{c})
}
