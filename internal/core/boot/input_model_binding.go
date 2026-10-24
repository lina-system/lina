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
	"strconv"
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
	// Prose in the selector names the only input this turn has. An event-shaped
	// id the turn did not consume stays unknown.
	if !bindingSelectorIsEventID(id) && s.bindingSource == nil && len(s.bindingInputs) == 0 {
		return s.input, s.runtime, nil
	}
	return ledger.Event{}, nil, &binding.ChoiceError{Field: "input_event_id", Reason: "unknown_input_event"}
}

func sameBindingSelector(selector string, event ledger.Event) bool {
	if selector == event.ID {
		return true
	}
	normalize := func(value string) string {
		return binding.Fold(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	}
	return selector != "" && normalize(selector) == normalize(event.Body.Text())
}

func bindingSelectorIsEventID(id string) bool {
	return id != "" && !strings.ContainsAny(id, " \t\r\n") && len(id) <= 256
}

// Keep the old input spelling readable for in-flight calls. Both selectors must
// resolve to the same admitted event if a caller supplies both.
func (s *turnState) selectedBindingInput(id, legacy string) (ledger.Event, *conversationRuntime, error) {
	if id == "" {
		return s.bindingInput(legacy)
	}
	input, r, err := s.bindingInput(id)
	if err != nil {
		return input, r, err
	}
	if legacy != "" {
		old, _, err := s.bindingInput(legacy)
		if err != nil {
			return ledger.Event{}, nil, err
		}
		if old.ID != input.ID {
			return ledger.Event{}, nil, &binding.ChoiceError{Field: "input_event_id", Reason: "conflicting_input_selectors"}
		}
	}
	return input, r, nil
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
			input, r, err := s.selectedBindingInput(in.InputEventID, in.Input)
			if err != nil {
				return bindingFailure(c.ID, err)
			}
			choices, err := r.saveModelBinding(ctx, input, "binding-choice:"+s.turnID+":"+c.ID, in.Bindings, "model")
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

func (r *conversationRuntime) saveModelBinding(ctx context.Context, input ledger.Event, id string, choices []binding.Choice, decider string) (out []binding.Choice, err error) {
	if input.PersonID != r.person || input.AgentID != r.agent || input.ConversationID != r.conversation || input.SpeakerKind != guard.Person || r.channelBinding != nil && (!r.channelBinding.Scope.Tools || r.validateChannel(ctx) != nil) {
		return nil, binding.ErrChoice
	}
	normalized, err := json.Marshal(choices)
	if err != nil {
		return nil, err
	}
	masked, err := secrets.Mask(string(normalized))
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(masked.Text()), &choices); err != nil {
		return nil, err
	}
	err = r.db.Write(ctx, func(tx *canon.Tx) error {
		// An immutable call is idempotent, including its originally pending card.
		var raw string
		e := tx.QueryRowContext(ctx, `SELECT body FROM conv_event WHERE id=? AND parent_id=? AND scaffold_kind=?`, id, input.ID, modelBindingKind).Scan(&raw)
		if e == nil {
			var batch bindingBatch
			if err := json.Unmarshal([]byte(raw), &batch); err != nil {
				return err
			}
			if len(batch.Bindings) != len(choices) {
				return binding.ErrChoice
			}
			for n, saved := range batch.Bindings {
				saved.Decider = ""
				saved.Question = ""
				saved.Probabilities = nil
				if !reflect.DeepEqual(saved, choices[n]) {
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
		state, e := r.bindingState(ctx, tx, input)
		if e != nil {
			return e
		}
		old, e := readInputChoices(ctx, tx, input)
		if e != nil {
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
				qid := targetQuestionID(input.ID, c.Request, 0)
				draft, e := targetQuestionDraft(state, *c)
				if e != nil {
					return e
				}
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

// Unknown binding fields are ignored. A missing or oversized call is still rejected.
func decodeBindingRequest(raw string) (bindingRequest, error) {
	var in bindingRequest
	if len(raw) > 32<<10 || json.Unmarshal([]byte(raw), &in) != nil || len(in.Bindings) == 0 || len(in.Bindings) > 32 {
		return in, binding.ErrChoice
	}
	// Core pages inherited targets. A model-supplied later page is ignored,
	// the same way as any binding field the tool does not declare.
	for i := range in.Bindings {
		in.Bindings[i].More = nil
	}
	return in, nil
}

func targetQuestionID(inputID string, request, page int) string {
	id := fmt.Sprintf("%starget:%s:%d", conversationQuestionPrefix, inputID, request)
	if page == 0 {
		return id
	}
	return fmt.Sprintf("%s:page:%d", id, page)
}

func targetQuestionPageNumber(id string) int {
	i := strings.LastIndex(id, ":page:")
	if i < 0 {
		return 0
	}
	page := id[i+len(":page:"):]
	n, err := strconv.Atoi(page)
	if err != nil || n < 1 || strconv.Itoa(n) != page {
		return 0
	}
	return n
}

func moreItemsLabel(used []string) string {
	const base = "Another item"
	if !slices.Contains(used, base) {
		return base
	}
	for n := 2; ; n++ {
		label := fmt.Sprintf("%s %d", base, n)
		if !slices.Contains(used, label) {
			return label
		}
	}
}

func targetQuestionDraft(state binding.State, c binding.Choice) (waiting.QuestionDraft, error) {
	if len(c.Tied) < 2 {
		return waiting.QuestionDraft{}, binding.ErrChoice
	}
	labels := make([]string, 0, len(c.Tied)+1)
	for _, target := range c.Tied {
		labels = append(labels, bindingChoiceLabel(state, target))
	}
	if len(c.More) > 0 {
		labels = append(labels, moreItemsLabel(labels))
	}
	draft := waiting.QuestionDraft{Question: "Which item should this apply to?", Recommendation: labels[0], Choices: labels, Default: labels[0]}
	if !draft.Valid() {
		return waiting.QuestionDraft{}, binding.ErrChoice
	}
	return draft, nil
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

func bindInputDeclaration() provider.ToolDecl {
	target := map[string]any{"type": "object", "properties": map[string]any{"class": map[string]any{"type": "string", "enum": []string{"existing", "new", "none"}}, "id": map[string]string{"type": "string", "description": "Required for existing: copy the candidate ID. Omit for new or none."}, "kind": map[string]string{"type": "string", "description": "For existing: copy the candidate kind. For new: initiative, project, todo, task or run. Omit for none."}, "parent": map[string]string{"type": "string", "description": "For new only: optional ID of an open initiative or project; omit for no parent."}}, "required": []string{"class"}, "additionalProperties": false}
	properties := map[string]any{"request": map[string]any{"type": "integer", "minimum": 0, "maximum": 31}, "quote": map[string]string{"type": "string"}, "target": target, "tied": map[string]any{"type": "array", "items": target, "minItems": 2, "maxItems": 5}, "action": map[string]any{"type": "string", "enum": []string{"read", "edit", "steer", "start", "send", "pay", "delete", "stop"}}, "close_matches": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}, "difference": map[string]string{"type": "string"}}
	raw, _ := json.Marshal(map[string]any{"type": "function", "name": "bind_input", "description": "Bind each request in the current input, in order from zero, using an exact quote and an authorized candidate, new (with kind and optional parent), or none. Split multiple requests even when a Core rule picked their parent. Use tied only when two targets remain equally plausible after reading the input and context, recommended first, and name the intended action. Core holds irreversible ties for a target question; never execute a held request. Otherwise pick the most likely target without asking. For new work mark close existing matches and record why it differs, or bind to that existing item. This grants no authority. Omit input_event_id for the current input; it selects an admitted event only when binding consumed steering. Work-tool input_binding uses the same selector and defaults to request 0.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"input_event_id": inputEventIDSchema(), "bindings": map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": map[string]any{"type": "object", "properties": properties, "required": []string{"request", "quote", "target"}, "additionalProperties": false}}}, "required": []string{"bindings"}, "additionalProperties": false}})
	return raw
}

// Explicit person answers close only the named request. No default applies.
// The extra answer on a full page asks the next page and leaves the request pending.
func (r *conversationRuntime) resolveTargetBinding(ctx context.Context, tx *canon.Tx, input ledger.Event, q waiting.ConversationQuestion, d waiting.QuestionResolution) error {
	if q.Binding == nil {
		return nil
	}
	c := *q.Binding
	c.Question = ""
	c.Decider = "person"
	c.Tied = nil
	c.More = nil
	if d.Kind == "answered" {
		index := slices.Index(q.Choices, d.Value)
		if index < 0 {
			return binding.ErrChoice
		}
		if index == len(q.Binding.Tied) && len(q.Binding.More) > 0 {
			return r.continueTargetQuestion(ctx, tx, input, q)
		}
		if index >= len(q.Binding.Tied) {
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

func (r *conversationRuntime) continueTargetQuestion(ctx context.Context, tx *canon.Tx, input ledger.Event, q waiting.ConversationQuestion) error {
	shown, rest := binding.QuestionPage(q.Binding.More)
	next := *q.Binding
	next.Target = shown[0]
	next.Tied = shown
	next.More = rest
	next.Question = ""
	next.Cancelled = false
	next.CreatedID = ""
	next.Probabilities = nil
	state, err := r.bindingState(ctx, tx, input)
	if err != nil {
		return err
	}
	if err = binding.ValidateChoice(state, input.Body.Text(), next); err != nil {
		return err
	}
	page := targetQuestionPageNumber(q.ID) + 1
	draft, err := targetQuestionDraft(state, next)
	if err != nil {
		return err
	}
	qid := targetQuestionID(input.ID, next.Request, page)
	// A later page has its own turn so it does not consume the input's question budget.
	turn := fmt.Sprintf("binding:%s:%d:page:%d", input.ID, next.Request, page)
	asked := waiting.ConversationQuestion{ID: qid, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: input.ID, Turn: turn, AskedAt: time.Now().UTC(), QuestionDraft: draft, Binding: &next}
	if err = waiting.NewConversationQuestions(questionTransaction{tx}).Ask(ctx, asked, 32); err != nil {
		return err
	}
	next.Question = qid
	next.Decider = "model"
	return appendInputChoices(ctx, tx, input, "binding-more:"+q.ID, []binding.Choice{next})
}

// publishContinuedTargetQuestion shows the next page after its answer commits.
// A runtime without a waiting topic still keeps the question for later restore.
func (r *conversationRuntime) publishContinuedTargetQuestion(ctx context.Context, q waiting.ConversationQuestion) error {
	if q.Binding == nil {
		return nil
	}
	input, err := r.journal().Get(ctx, q.Input)
	if err != nil {
		return err
	}
	choices, err := r.currentInputChoices(ctx, input)
	if err != nil {
		return err
	}
	if q.Binding.Request < 0 || q.Binding.Request >= len(choices) {
		return nil
	}
	id := choices[q.Binding.Request].Question
	if id == "" || id == q.ID {
		return nil
	}
	decisions := r.questionDecisions()
	if decisions == nil || decisions.Questions == nil || decisions.Topics == nil {
		return nil
	}
	return decisions.PublishQuestion(ctx, id)
}
