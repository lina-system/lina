// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/binding"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

const conversationQuestionPrefix = "conversation-question:"

var errQuestionWithdrawn = errors.New("question_withdrawn")

func (r *conversationRuntime) questionDecisions() *clientsrv.Decisions {
	if r.router != nil {
		return r.router.root.decisions
	}
	return r.decisions
}

type askPreference struct {
	PerTurn    int    `json:"per_turn"`
	Form       string `json:"form"`
	Unanswered string `json:"unanswered"`
}

func decodeQuestion(raw string) (waiting.QuestionDraft, error) {
	var q waiting.QuestionDraft
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 32<<10 || d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF || !q.Valid() {
		return q, waiting.ErrInvalid
	}
	return q, nil
}
func (r *conversationRuntime) prepareQuestionTool(ctx context.Context, turnID string, input ledger.Event, method agents.WorkMethodSettings, bindings map[string]loop.Tool, gate *questionTurn) (assemble.ContextItem, error) {
	var ask askPreference
	for key, target := range map[string]any{"ask.per_turn": &ask.PerTurn, "ask.form": &ask.Form, "ask.unanswered": &ask.Unanswered} {
		if err := json.Unmarshal(method.Values[key], target); err != nil {
			return assemble.ContextItem{}, err
		}
	}
	s := waiting.NewConversationQuestions(r.db)
	pending, err := s.Pending(ctx, r.person, r.agent)
	if err != nil {
		return assemble.ContextItem{}, err
	}
	visibility, err := r.removals.Snapshot(ctx, r.person, r.agent)
	if err != nil {
		return assemble.ContextItem{}, err
	}
	visible := pending[:0]
	var evidence []assemble.Ref
	for _, q := range pending {
		allowed, err := r.questionContextAllowed(ctx, q)
		if err != nil {
			return assemble.ContextItem{}, err
		}
		if allowed && visibility.Visible(retract.Ref{Kind: retract.Event, ID: q.Input}) {
			visible = append(visible, q)
			evidence = append(evidence, assemble.Ref{ID: q.Input})
		}
	}
	pending = visible
	gate.init(r, input, pending)
	bindings["resolve_question"] = gate.tool()
	body, err := json.Marshal(struct {
		Kind    string                         `json:"kind"`
		Ask     askPreference                  `json:"ask"`
		Pending []waiting.ConversationQuestion `json:"pending_questions,omitempty"`
	}{"working_preference", ask, pending})
	if err != nil {
		return assemble.ContextItem{}, err
	}
	bindings["ask_question"] = loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			_, err := decodeQuestion(c.Arguments)
			return "questions:" + turnID, err
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			draft, err := decodeQuestion(c.Arguments)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Invalid}
			}
			q := waiting.ConversationQuestion{ID: conversationQuestionPrefix + turnID + ":" + c.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID, Input: input.ID, AskedAt: time.Now().UTC(), QuestionDraft: draft}
			if err = s.Ask(ctx, q, ask.PerTurn); err != nil {
				if errors.Is(err, waiting.ErrQuestionLimit) {
					return loop.Result{ID: c.ID, Code: loop.Invalid, Output: json.RawMessage(`{"code":"question_limit"}`)}
				}
				return loop.Result{ID: c.ID, Code: loop.Failed, Output: json.RawMessage(`{"code":"question_not_saved"}`)}
			}
			if err = r.questionDecisions().PublishQuestion(ctx, q.ID); err != nil {
				return loop.Result{ID: c.ID, Code: loop.Failed, Output: json.RawMessage(`{"code":"question_not_published"}`)}
			}
			saved, err := s.Get(ctx, q.ID)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Failed}
			}
			raw, _ := json.Marshal(saved)
			return loop.Result{ID: c.ID, Code: loop.OK, Output: raw}
		},
	}
	return assemble.ContextItem{ID: "working-preference:" + r.agent, AgentID: r.agent, Kind: assemble.Commitments, Origin: "conversation-questions:" + r.agent, Text: string(body), Evidence: evidence}, nil
}

// A shared conversation ID does not grant a channel the app's or another
// thread's pending questions. Resolve the retained receipt to its exact address.
func (r *conversationRuntime) questionContextAllowed(ctx context.Context, q waiting.ConversationQuestion) (bool, error) {
	if r.channelBinding == nil {
		return true, nil
	}
	if !r.channelBinding.Scope.Tools {
		return false, nil
	}
	if r.channelBinding.Scope.Memory {
		return true, nil
	}
	input, err := r.journal().Get(ctx, q.Input)
	if err != nil {
		return false, err
	}
	if input.Surface.Kind != "channel" {
		return false, nil
	}
	root := r
	if r.router != nil {
		root = r.router.root
	}
	origin, err := root.workerCompletionView(ctx, ledger.Event{ParentID: input.ID, AgentID: q.Agent, ConversationID: q.Conversation, Surface: input.Surface})
	if errors.Is(err, channels.ErrDenied) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return origin.channelAddress == r.channelAddress, nil
}
func askQuestionDeclaration() provider.ToolDecl {
	return provider.ToolDecl(`{"type":"function","name":"ask_question","description":"Ask one question that changes the result, with a recommendation and the default you will use if the person moves on. Follow the working_preference ask form and per-turn limit. Optional choices are short situational alternatives. This records a general conversation question. A skipped answer cannot bind an unresolved instruction target or grant permission for an external effect. Return to the conversation while it is pending.","parameters":{"type":"object","properties":{"yes_no":{"type":"boolean","description":"The question explicitly asks yes or no."},"question":{"type":"string"},"recommendation":{"type":"string"},"choices":{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":5},"default":{"type":"string"}},"required":["question","recommendation","default"],"additionalProperties":false}}`)
}
func (r *conversationRuntime) conversationQuestionCard(ctx context.Context, person, id string) (protocol.Question, error) {
	q, err := waiting.NewConversationQuestions(r.db).Get(ctx, id)
	if err != nil {
		return protocol.Question{}, err
	}
	if q.Person != person {
		return protocol.Question{}, waiting.ErrPerson
	}
	card := &protocol.DecisionCard{ID: q.ID, Revision: "1", Agent: r.questionDecisions().AgentName(q.Agent), Summary: q.Question, Recommendation: &q.Recommendation, Targets: []protocol.DecisionTarget{{Name: "Default", Value: q.Default}}}
	if q.Binding != nil {
		card.Targets = []protocol.DecisionTarget{}
	}
	visible, err := r.questionVisible(ctx, q)
	if err != nil {
		return protocol.Question{}, err
	}
	if !visible {
		outcome := protocol.DecisionOutcomeWithdrawn
		card.Outcome = &outcome
		return protocol.Question{ConversationID: q.Conversation, Card: card}, nil
	}
	field := protocol.FormField{Name: "answer", Type: protocol.FormFieldTypeText}
	if len(q.Choices) > 0 {
		field.Type = protocol.FormFieldTypeChoice
		field.Choices = &q.Choices
	}
	if q.Resolution != nil {
		outcome := protocol.DecisionOutcomeResolved
		card.Outcome = &outcome
	}
	if _, err := r.agents.Get(ctx, q.Agent); errors.Is(err, agents.ErrNotFound) {
		outcome := protocol.DecisionOutcomeWithdrawn
		card.Outcome = &outcome
	} else if err != nil {
		return protocol.Question{}, err
	}
	return protocol.Question{ConversationID: q.Conversation, Card: card, Form: &protocol.QuestionForm{PluginName: "LINA", Fields: []protocol.FormField{field}}}, nil
}
func (r *conversationRuntime) answerConversationQuestion(ctx context.Context, person string, a protocol.QuestionAnswer, messageID string) (protocol.DecisionOutcome, error) {
	if a.CardID == nil || a.Revision == nil || *a.Revision != "1" {
		return protocol.DecisionOutcomeStaleGroup, nil
	}
	s := waiting.NewConversationQuestions(r.db)
	q, err := s.Get(ctx, *a.CardID)
	if err != nil {
		return "", err
	}
	if q.Person != person {
		return protocol.DecisionOutcomeRefused, nil
	}
	if _, err = r.agents.Get(ctx, q.Agent); err != nil {
		return protocol.DecisionOutcomeWithdrawn, nil
	}
	visible, err := r.questionVisible(ctx, q)
	if err != nil {
		return "", err
	}
	if !visible {
		return protocol.DecisionOutcomeWithdrawn, nil
	}
	if q.Resolution != nil {
		return protocol.DecisionOutcomeResolved, nil
	}
	d := waiting.QuestionResolution{Input: messageID, At: time.Now().UTC()}
	switch a.Answer {
	case protocol.QuestionAnswerActionAccept:
		if a.Values == nil || len(*a.Values) != 1 {
			return protocol.DecisionOutcomeRefused, nil
		}
		value := (*a.Values)["answer"]
		if value.String == nil {
			return protocol.DecisionOutcomeRefused, nil
		}
		d.Kind, d.Value = "answered", *value.String
	case protocol.QuestionAnswerActionDecline:
		d.Kind = "declined"
	case protocol.QuestionAnswerActionCancel:
		d.Kind = "cancelled"
	default:
		return protocol.DecisionOutcomeRefused, nil
	}
	input, err := r.journal().Get(ctx, q.Input)
	if err != nil {
		return "", err
	}
	var answerEvent ledger.Event
	var receipt inbox.Receipt
	if err = s.ResolveWith(ctx, person, q.Agent, q.ID, d, func(ctx context.Context, tx *canon.Tx, q waiting.ConversationQuestion, d waiting.QuestionResolution) error {
		visible, err := waiting.ConversationQuestionVisible(ctx, tx, q)
		if err != nil {
			return err
		}
		if !visible {
			return errQuestionWithdrawn
		}
		if err := resolveTargetBinding(ctx, tx, input, q, d); err != nil {
			return err
		}
		value := d.Value
		if d.Kind == "declined" {
			value = design.Text(eventLanguage(input), "decline")
		}
		if d.Kind == "cancelled" {
			value = design.Text(eventLanguage(input), "notice.run_cancelled")
		}
		body := q.Question + "\n" + design.Text(eventLanguage(input), "notice.question_answer") + value
		if q.Binding != nil {
			choices, err := readInputChoices(ctx, tx, input)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(choices[q.Binding.Request])
			body += "\nHeld request: " + string(raw) + "\nResume only the explicitly selected request after checking its current state and authority."
		}
		masked, err := secrets.Mask(body)
		if err != nil {
			return err
		}
		receipt, err = inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: q.Conversation, Source: "question-answer", NativeID: q.ID + ":answer"}, Body: []byte(masked.Text())})
		if err != nil {
			return err
		}
		answerEvent, err = ledger.New(questionTransaction{tx}, nil).Admit(ctx, guard.EntrySystem, ledger.Input{ID: q.ID + ":answer", ConversationID: q.Conversation, ParentID: q.Input, AgentID: q.Agent, Surface: input.Surface, ScaffoldKind: "question_answer", OccurredAt: d.At, RecordedAt: d.At, TimeQuality: "observed", Body: body, Relations: []ledger.Relation{{TargetID: q.Input, Kind: "view_of"}}})
		return err
	}); err != nil {
		if errors.Is(err, errQuestionWithdrawn) {
			return protocol.DecisionOutcomeWithdrawn, nil
		}
		if errors.Is(err, waiting.ErrInvalid) || errors.Is(err, waiting.ErrAnswered) {
			return protocol.DecisionOutcomeRefused, nil
		}
		return "", err
	}
	if receipt.Inserted && r.route != nil {
		view, err := r.workerCompletionView(ctx, answerEvent)
		if err != nil {
			return "", err
		}
		if err = view.submitWorkerCompletion(answerEvent, receipt.ID); err != nil {
			return "", err
		}
	}
	return protocol.DecisionOutcomeResolved, nil
}
func (r *conversationRuntime) restoreConversationQuestions(ctx context.Context) error {
	questions, err := waiting.NewConversationQuestions(r.db).Pending(ctx, r.person, "")
	if err != nil {
		return err
	}
	for _, q := range questions {
		if err = r.questionDecisions().PublishQuestion(ctx, q.ID); err != nil {
			return err
		}
	}
	return nil
}

// Keep JSON decoding strict for question decisions as well as question drafts.
func decodeQuestionJSON(raw string, out any) error {
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if len(raw) > 32<<10 || d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return waiting.ErrInvalid
	}
	return nil
}

// Relatedness is a model judgment about this confirmed person input. Core owns
// closure and refuses work/completion until each older question is accounted for.
type questionTurn struct {
	mu       sync.Mutex
	runtime  *conversationRuntime
	pending  map[string]bool
	assessed map[string]string // question ID -> latest assessed input ID
	sources  map[string][]ledger.Event
}

func (g *questionTurn) init(r *conversationRuntime, input ledger.Event, pending []waiting.ConversationQuestion) {
	g.runtime = r
	g.pending = map[string]bool{}
	g.assessed = map[string]string{}
	g.sources = map[string][]ledger.Event{}
	if !g.personInput(input) {
		return
	}
	for _, q := range pending {
		if q.Input != input.ID {
			g.pending[q.ID] = true
			g.sources[q.ID] = []ledger.Event{input}
		}
	}
}

func (g *questionTurn) personInput(input ledger.Event) bool {
	r := g.runtime
	return input.SpeakerKind == guard.Person && input.PersonID == r.person && input.AgentID == r.agent && input.ConversationID == r.conversation && input.Origin.Kind == "native" && input.ReplayOf == ""
}

// Only admission's durable event, consumed at a round boundary, can supply a
// new answer. Refresh here so questions asked during this turn are eligible,
// without treating the input that caused a question as an answer to it.
func (g *questionTurn) consume(ctx context.Context, accepted acceptedSteering) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	input, r := accepted.event, g.runtime
	if !g.personInput(input) {
		return nil
	}
	pending, err := waiting.NewConversationQuestions(r.db).Pending(ctx, r.person, r.agent)
	if err != nil {
		return err
	}
	visibility, err := r.removals.Snapshot(ctx, r.person, r.agent)
	if err != nil {
		return err
	}
	if !visibility.Visible(retract.Ref{Kind: retract.Event, ID: input.ID}) {
		return nil
	}
	for _, q := range pending {
		if q.Input == input.ID || !visibility.Visible(retract.Ref{Kind: retract.Event, ID: q.Input}) {
			continue
		}
		allowed, err := r.questionContextAllowed(ctx, q)
		if err != nil {
			return err
		}
		if allowed && accepted.runner != nil {
			// Sharing a conversation does not widen the steering sender's scope.
			allowed, err = accepted.runner.questionContextAllowed(ctx, q)
			if err != nil {
				return err
			}
		}
		if allowed {
			g.pending[q.ID] = true
			g.sources[q.ID] = append(g.sources[q.ID], input)
		}
	}
	return nil
}
func (g *questionTurn) check(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id := range g.pending {
		sources := g.sources[id]
		if len(sources) > 0 && g.assessed[id] == sources[len(sources)-1].ID {
			continue
		}
		q, err := waiting.NewConversationQuestions(g.runtime.db).Get(ctx, id)
		if err != nil {
			return err
		}
		if q.Resolution == nil && q.Binding == nil {
			return errors.New("pending_question_needs_resolution")
		}
	}
	return nil
}
func (g *questionTurn) before(ctx context.Context, c provider.ToolCall) *loop.Result {
	if c.Name == "resolve_question" {
		return nil
	}
	if err := g.check(ctx); err != nil {
		return &loop.Result{ID: c.ID, Code: loop.Invalid, Output: json.RawMessage(`{"code":"pending_question_needs_resolution"}`)}
	}
	return nil
}

type questionDecisionInput struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Quote string `json:"quote"`
	Value string `json:"value,omitempty"`
}

// The caller holds mu. Prefer the most recently consumed matching source when
// two owner inputs contain the same quote; never combine bytes across inputs.
func (g *questionTurn) decode(raw string) (questionDecisionInput, ledger.Event, error) {
	var in questionDecisionInput
	var input ledger.Event
	if decodeQuestionJSON(raw, &in) != nil || strings.TrimSpace(in.Quote) == "" || !g.pending[in.ID] {
		return in, input, waiting.ErrInvalid
	}
	sources := g.sources[in.ID]
	for i := len(sources) - 1; i >= 0; i-- {
		if strings.Contains(sources[i].Body.Text(), in.Quote) {
			input = sources[i]
			break
		}
	}
	if input.ID == "" {
		return in, input, waiting.ErrInvalid
	}
	switch in.Kind {
	case "related", "default_applied":
		if in.Value != "" {
			return in, input, waiting.ErrInvalid
		}
	case "answered":
		if strings.TrimSpace(in.Value) == "" {
			return in, input, waiting.ErrInvalid
		}
	default:
		return in, input, waiting.ErrInvalid
	}
	return in, input, nil
}
func (g *questionTurn) tool() loop.Tool {
	return loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			in, _, err := g.decode(c.Arguments)
			return in.ID, err
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			g.mu.Lock()
			defer g.mu.Unlock()
			in, input, err := g.decode(c.Arguments)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Invalid}
			}
			r := g.runtime
			s := waiting.NewConversationQuestions(r.db)
			q, err := s.Get(ctx, in.ID)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Failed}
			}
			visible, err := r.questionVisible(ctx, q)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Failed}
			}
			if !visible {
				return loop.Result{ID: c.ID, Code: loop.Invalid, Output: json.RawMessage(`{"code":"question_withdrawn"}`)}
			}
			if q.Binding != nil && in.Kind != "related" {
				if in.Kind != "answered" {
					return bindingFailure(c.ID, errBindingPending)
				}
				index := -1
				for n, label := range q.Choices {
					if binding.Fold(strings.TrimSpace(in.Quote)) == binding.Fold(label) || strings.TrimSpace(in.Quote) == strconv.Itoa(n+1) {
						index = n
					}
				}
				if index < 0 || in.Value != q.Choices[index] {
					return bindingFailure(c.ID, binding.ErrChoice)
				}
				revision, value := "1", q.Choices[index]
				values := protocol.FormValues{"answer": {String: &value}}
				outcome, err := r.answerConversationQuestion(ctx, r.person, protocol.QuestionAnswer{CardID: &q.ID, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}, input.ID)
				if err != nil || outcome != protocol.DecisionOutcomeResolved {
					return loop.Result{ID: c.ID, Code: loop.Invalid}
				}
				q, err = s.Get(ctx, q.ID)
				if err != nil {
					return loop.Result{ID: c.ID, Code: loop.Failed}
				}
			}
			if q.Resolution == nil && in.Kind != "related" {
				d := waiting.QuestionResolution{Kind: in.Kind, Input: input.ID, Value: in.Value, At: time.Now().UTC()}
				if in.Kind == "default_applied" {
					d.Value = q.Default
				}
				if err = s.ResolveWith(ctx, r.person, r.agent, q.ID, d, func(ctx context.Context, tx *canon.Tx, q waiting.ConversationQuestion, d waiting.QuestionResolution) error {
					visible, err := waiting.ConversationQuestionVisible(ctx, tx, q)
					if err != nil {
						return err
					}
					if !visible {
						return errQuestionWithdrawn
					}
					if d.Kind != "default_applied" {
						return nil
					}
					_, err = ledger.New(questionTransaction{tx}, nil).Admit(ctx, guard.EntrySystem, r.questionDefaultInput(input, q, d))
					return err
				}); err != nil {
					return loop.Result{ID: c.ID, Code: loop.Invalid}
				}
				q, err = s.Get(ctx, q.ID)
				if err != nil {
					return loop.Result{ID: c.ID, Code: loop.Failed}
				}
			}
			if q.Resolution != nil && q.Resolution.Kind == "default_applied" && q.Resolution.Input == input.ID {
				if err = r.questionDefaultNotice(ctx, input, q); err != nil {
					return loop.Result{ID: c.ID, Code: loop.Failed}
				}
			}
			if err = r.questionDecisions().PublishQuestion(ctx, q.ID); err != nil {
				return loop.Result{ID: c.ID, Code: loop.Failed}
			}
			// An older related follow-up cannot account for newer steering.
			// Reprocessing it must also preserve an assessed newer input.
			sources := g.sources[q.ID]
			if sources[len(sources)-1].ID == input.ID {
				g.assessed[q.ID] = input.ID
			}
			body, _ := json.Marshal(q)
			return loop.Result{ID: c.ID, Code: loop.OK, Output: body}
		},
	}
}
func resolveQuestionDeclaration() provider.ToolDecl {
	return provider.ToolDecl(`{"type":"function","name":"resolve_question","description":"Before other tools or your final reply, account for each pending question using the current person input or owner steering received during this turn. Quote the input that answers it or changes the topic. A related assessment must cover the latest consumed input before work can continue; quoting an older follow-up does not account for newer steering. Use answered with the person's value, related if they ask a related follow-up without answering, or default_applied if they move to an unrelated topic. For a binding question, quote an exact offered label or number and use its label as value; unrelated turns leave it pending and default_applied is refused. Other questions apply the stored default, close and record a notice. Continue the original work with that value. No resolution grants effect authority.","parameters":{"type":"object","properties":{"id":{"type":"string"},"kind":{"type":"string","enum":["answered","related","default_applied"]},"quote":{"type":"string"},"value":{"type":"string"}},"required":["id","kind","quote"],"additionalProperties":false}}`)
}

type questionTransaction struct{ tx *canon.Tx }

func (t questionTransaction) Write(_ context.Context, f func(*canon.Tx) error) error { return f(t.tx) }
func (t questionTransaction) Read(_ context.Context, f func(*canon.Tx) error) error  { return f(t.tx) }
func (r *conversationRuntime) questionDefaultInput(input ledger.Event, q waiting.ConversationQuestion, d waiting.QuestionResolution) ledger.Input {
	return ledger.Input{ID: q.ID + ":default:" + input.ID, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: "question_default", OccurredAt: d.At, RecordedAt: d.At, TimeQuality: "observed", Body: design.Text(eventLanguage(input), "notice.question_default") + q.Default, Relations: []ledger.Relation{{TargetID: q.Input, Kind: "view_of"}}}
}
func (r *conversationRuntime) questionDefaultNotice(ctx context.Context, input ledger.Event, q waiting.ConversationQuestion) error {
	event, err := r.journal().Get(ctx, q.ID+":default:"+input.ID)
	if err != nil {
		return err
	}
	text, speaker := event.Body.Text(), protocol.ConversationSpeakerSystem
	return r.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
}

func (r *conversationRuntime) questionVisible(ctx context.Context, q waiting.ConversationQuestion) (visible bool, err error) {
	err = r.db.Read(ctx, func(tx *canon.Tx) error { visible, err = waiting.ConversationQuestionVisible(ctx, tx, q); return err })
	return
}
