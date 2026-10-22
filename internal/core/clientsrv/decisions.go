// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/kit/protocol"
)

var DecisionsCapability = protocol.Capability{Name: "waiting", Version: "1.0"}

// QuestionSource adapts the owning Core service. It owns the pending question
// and atomically validates revision/owner when recording its answer. This
// listener stores no answers and never interprets a form as an approval.
type QuestionSource interface {
	Question(context.Context, string, string) (protocol.Question, error)
	AnswerQuestion(context.Context, string, protocol.QuestionAnswer) (protocol.DecisionOutcome, error)
}

// AttributedQuestionSource retains the authenticated transport message identity
// with decisions whose owning ledger needs replay-safe goal revisions.
type AttributedQuestionSource interface {
	AnswerQuestionMessage(context.Context, string, protocol.QuestionAnswer, string) (protocol.DecisionOutcome, error)
}

type Decisions struct {
	presenceMu sync.Mutex
	present    map[string]bool
	// This cache is the last published projection only; authority owns decisions.
	publishMu sync.Mutex
	published map[string]protocol.ApprovalRequest
	Authority *authority.Service
	Topics    *Topics
	Person    string
	AgentName func(string) string
	Questions QuestionSource
}

func (d *Decisions) Operations() map[protocol.Kind]Operation {
	operations := map[protocol.Kind]Operation{}
	if d.Authority != nil {
		operations[protocol.KindApprovalAnswer] = Operation{Capability: DecisionsCapability, Handle: d.approvalAnswer}
	}
	if d.Questions != nil {
		operations[protocol.KindQuestionAnswer] = Operation{Capability: DecisionsCapability, Handle: d.questionAnswer}
	}
	return operations
}
func (d *Decisions) eligible(session Session) bool {
	return session.CanDecide && session.PersonID != "" && session.PersonID == d.Person && hasCapability(session.Capabilities, DecisionsCapability)
}
func (d *Decisions) ApprovalCards(ctx context.Context) ([]protocol.ApprovalRequest, error) {
	if d.Authority == nil {
		return nil, &OperationError{Code: protocol.ErrorUnavailable}
	}
	groups, err := d.Authority.Groups(ctx, d.Person)
	if err != nil {
		return nil, err
	}
	cards := make([]protocol.ApprovalRequest, 0, len(groups))
	for _, g := range groups {
		c := protocol.DecisionCard{ID: g.ID, Revision: g.Revision, Targets: []protocol.DecisionTarget{}}
		var names, summaries []string
		choices := slices.Clone(g.Members[0].Choices)
		for i, a := range g.Members {
			name := a.Agent
			if d.AgentName != nil {
				name = d.AgentName(a.Agent)
			}
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
			if !slices.Contains(summaries, a.What) {
				summaries = append(summaries, a.What)
			}
			prefix := strconv.Itoa(i+1) + " "
			c.Targets = append(c.Targets, protocol.DecisionTarget{Name: prefix + "agent", Value: name}, protocol.DecisionTarget{Name: prefix + "what", Value: a.What}, protocol.DecisionTarget{Name: prefix + "where", Value: a.Where}, protocol.DecisionTarget{Name: prefix + "target", Value: a.Target}, protocol.DecisionTarget{Name: prefix + "undoable", Value: strconv.FormatBool(a.Undoable)})
			if len(a.Arguments) > 0 {
				c.Targets = append(c.Targets, protocol.DecisionTarget{Name: prefix + "arguments", Value: string(a.Arguments)})
			}
			choices = slices.DeleteFunc(choices, func(choice authority.Choice) bool { return !slices.Contains(a.Choices, choice) })
		}
		c.Agent = strings.Join(names, ", ")
		c.Summary = strings.Join(summaries, "; ")
		request := protocol.ApprovalRequest{Card: &c, Choices: []protocol.ApprovalChoice{}}
		if conversation := g.Members[0].Conversation; conversation != "" {
			request.ConversationID = &conversation
		}
		for _, choice := range choices {
			switch choice {
			case authority.Once:
				request.Choices = append(request.Choices, protocol.ApprovalChoiceOnce)
			case authority.Deny:
				request.Choices = append(request.Choices, protocol.ApprovalChoiceDeny)
			case authority.Always:
				request.Choices = append(request.Choices, protocol.ApprovalChoiceAlwaysAllow)
			}
		}
		if len(request.Choices) == 0 {
			return nil, &OperationError{Code: protocol.ErrorMalformed}
		}
		cards = append(cards, request)
	}
	return cards, nil
}
func (d *Decisions) approvalAnswer(ctx context.Context, session Session, envelope protocol.Envelope) (Result, error) {
	if !d.eligible(session) {
		return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
	}
	answer, ok := envelope.Payload.(protocol.ApprovalAnswer)
	if !ok || answer.CardID == nil || *answer.CardID == "" || answer.Revision == nil || *answer.Revision == "" || answer.Outcome != nil {
		return Result{}, &OperationError{Code: protocol.ErrorMalformed}
	}
	var choice authority.Choice
	switch answer.Choice {
	case protocol.ApprovalChoiceOnce:
		choice = authority.Once
	case protocol.ApprovalChoiceDeny:
		choice = authority.Deny
	case protocol.ApprovalChoiceAlwaysAllow:
		choice = authority.Always
	default:
		return Result{}, &OperationError{Code: protocol.ErrorMalformed}
	}
	decider := authority.Decider{Person: session.PersonID, Epoch: session.Epoch, Surface: authority.App, Authenticated: session.CanDecide}
	err := d.Authority.Answer(ctx, *answer.CardID, decider, authority.Decision{Choice: choice, GroupRevision: *answer.Revision})
	outcome := protocol.DecisionOutcomeResolved
	switch {
	case err == nil:
	case errors.Is(err, authority.ErrStale):
		a, readErr := d.Authority.Approval(ctx, *answer.CardID)
		switch {
		case errors.Is(readErr, sql.ErrNoRows):
			outcome = protocol.DecisionOutcomeWithdrawn
		case readErr != nil:
			return Result{}, readErr
		case a.Person != session.PersonID:
			return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
		case a.Epoch != session.Epoch:
			outcome = protocol.DecisionOutcomeStaleEpoch
		case a.State == authority.Stopped:
			outcome = protocol.DecisionOutcomeStopped
		case a.State != authority.Pending:
			outcome = protocol.DecisionOutcomeAlreadyResolved
		default:
			outcome = protocol.DecisionOutcomeStaleGroup
		}
	case errors.Is(err, authority.ErrDecider), errors.Is(err, authority.ErrChoice):
		outcome = protocol.DecisionOutcomeRefused
	default:
		return Result{}, err
	}
	// Publish the current source, even when another surface won. A publication
	// failure cannot undo a committed answer or make the client repeat it.
	_ = d.PublishApprovals(ctx)
	answer.Outcome = &outcome
	return Result{Kind: protocol.KindApprovalAnswer, Payload: answer}, nil
}

func (d *Decisions) PublishQuestion(ctx context.Context, id string) error {
	if d.Questions == nil || d.Topics == nil {
		return &OperationError{Code: protocol.ErrorUnavailable}
	}
	question, err := d.Questions.Question(ctx, d.Person, id)
	if err != nil {
		return err
	}
	if question.Card == nil || question.Card.ID != id || question.Card.Revision == "" {
		return &OperationError{Code: protocol.ErrorMalformed}
	}
	if !safeQuestionForm(question.Form) {
		outcome := protocol.DecisionOutcomeRefused
		card := *question.Card
		card.Outcome = &outcome
		question.Card = &card
		question.Form = nil
	}
	data, err := json.Marshal(question)
	if err != nil {
		return err
	}
	return d.Topics.Publish("waiting", TopicEvent{Kind: protocol.KindQuestion, Key: id, Data: data})
}
func (d *Decisions) questionAnswer(ctx context.Context, session Session, envelope protocol.Envelope) (Result, error) {
	if !d.eligible(session) {
		return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
	}
	answer, ok := envelope.Payload.(protocol.QuestionAnswer)
	if !ok || answer.CardID == nil || answer.Revision == nil || answer.Outcome != nil {
		return Result{}, &OperationError{Code: protocol.ErrorMalformed}
	}
	question, err := d.Questions.Question(ctx, session.PersonID, *answer.CardID)
	if err != nil {
		return Result{}, err
	}
	outcome := protocol.DecisionOutcomeRefused
	switch {
	case question.Card == nil || question.Card.ID != *answer.CardID:
		return Result{}, &OperationError{Code: protocol.ErrorMalformed}
	case question.Card.Revision != *answer.Revision:
		outcome = protocol.DecisionOutcomeStaleGroup
	case question.Card.Outcome != nil:
		outcome = *question.Card.Outcome
	case !safeQuestionForm(question.Form):
	case !validQuestionAnswer(question.Form, answer):
		return Result{}, &OperationError{Code: protocol.ErrorMalformed}
	default:
		if source, ok := d.Questions.(AttributedQuestionSource); ok {
			outcome, err = source.AnswerQuestionMessage(ctx, session.PersonID, answer, envelope.MessageID)
		} else {
			outcome, err = d.Questions.AnswerQuestion(ctx, session.PersonID, answer)
		}
		if err != nil {
			return Result{}, err
		}
		if outcome == protocol.DecisionOutcomeResolved {
			// The source committed the answer. Publication is a projection: a closed
			// topic must not turn an accepted decision into a failed answer or replay.
			// A new connection seeds its projection from the same question source.
			_ = d.PublishQuestion(ctx, *answer.CardID)
		}
	}
	answer.Outcome = &outcome
	return Result{Kind: protocol.KindQuestionAnswer, Payload: answer}, nil
}

// The source also validates elicitation against its plugin contract. This
// boundary refuses known secret-field names even for an incorrectly wired
// source. It never accepts a password/key/token/payment form from a client.
func safeQuestionForm(form *protocol.QuestionForm) bool {
	if form == nil {
		return false
	}
	seen := map[string]bool{}
	for _, field := range form.Fields {
		name := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(field.Name))
		switch name {
		case "password", "passwd", "secret", "api_key", "apikey", "key", "token", "access_token", "refresh_token", "credit_card", "card_number", "cvv", "payment", "payment_details":
			return false
		}
		if name == "" || seen[field.Name] {
			return false
		}
		seen[field.Name] = true
	}
	return true
}
func validQuestionAnswer(form *protocol.QuestionForm, answer protocol.QuestionAnswer) bool {
	if answer.Answer == protocol.QuestionAnswerActionDecline || answer.Answer == protocol.QuestionAnswerActionCancel {
		return answer.Values == nil
	}
	if answer.Answer != protocol.QuestionAnswerActionAccept || answer.Values == nil || form == nil || len(*answer.Values) != len(form.Fields) {
		return false
	}
	for _, field := range form.Fields {
		value, ok := (*answer.Values)[field.Name]
		if !ok {
			return false
		}
		if _, err := json.Marshal(value); err != nil {
			return false
		}
		switch field.Type {
		case protocol.FormFieldTypeText:
			if value.String == nil {
				return false
			}
		case protocol.FormFieldTypeNumber:
			if value.Number == nil {
				return false
			}
		case protocol.FormFieldTypeBoolean:
			if value.Boolean == nil {
				return false
			}
		case protocol.FormFieldTypeChoice:
			if value.String == nil || field.Choices == nil {
				return false
			}
			found := false
			for _, choice := range *field.Choices {
				found = found || choice == *value.String
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func (d *Decisions) PublishApprovals(ctx context.Context) error {
	d.publishMu.Lock()
	defer d.publishMu.Unlock()
	if d.Topics == nil {
		return &OperationError{Code: protocol.ErrorUnavailable}
	}
	cards, err := d.ApprovalCards(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]protocol.ApprovalRequest, len(cards))
	publish := func(card protocol.ApprovalRequest) error {
		data, err := json.Marshal(card)
		if err != nil {
			return err
		}
		return d.Topics.Publish("waiting", TopicEvent{Kind: protocol.KindApprovalRequest, Key: card.Card.ID, Data: data})
	}
	for _, card := range cards {
		next[card.Card.ID] = card
	}
	for id, previous := range d.published {
		if _, ok := next[id]; ok {
			continue
		}
		a, err := d.Authority.Approval(ctx, id)
		outcome := protocol.DecisionOutcomeWithdrawn
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			switch a.State {
			case authority.Approved, authority.Denied, authority.Consumed:
				outcome = protocol.DecisionOutcomeResolved
			case authority.Stopped:
				outcome = protocol.DecisionOutcomeStopped
			case authority.Pending:
				outcome = protocol.DecisionOutcomeStaleGroup
			}
		}
		card := *previous.Card
		card.Outcome = &outcome
		previous.Card = &card
		if err := publish(previous); err != nil {
			return err
		}
	}
	for _, card := range cards {
		if previous, ok := d.published[card.Card.ID]; ok {
			old, _ := json.Marshal(previous)
			fresh, _ := json.Marshal(card)
			if bytes.Equal(old, fresh) {
				continue
			}
		}
		if err := publish(card); err != nil {
			return err
		}
	}
	d.published = next
	return nil
}

// Presence tracks only the lifetime of admitted waiting-capable owner clients.
// Approval ownership, epochs and choice validation remain in authority.Answer.
func (d *Decisions) Presence(session Session, connected bool) {
	d.presenceMu.Lock()
	defer d.presenceMu.Unlock()
	if !connected {
		delete(d.present, session.ConnectionID)
		return
	}
	if session.ConnectionID == "" || !d.eligible(session) {
		return
	}
	if d.present == nil {
		d.present = map[string]bool{}
	}
	d.present[session.ConnectionID] = true
}
func (d *Decisions) CanPresent() bool {
	d.presenceMu.Lock()
	defer d.presenceMu.Unlock()
	return len(d.present) > 0
}

// ApprovalsChanged observes a durable commit, not an in-flight client request.
// Preserve its values but detach cancellation/deadline: a disconnect cannot
// undo publication. A bounded failure invalidates the stream so subscribers
// reconcile through the existing refresh-on-subscribe path.
func (d *Decisions) ApprovalsChanged(ctx context.Context) {
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := d.PublishApprovals(publishCtx); err != nil && d.Topics != nil {
		d.Topics.RequireResync("waiting")
	}
}
