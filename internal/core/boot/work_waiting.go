// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) startWorkWaiting(ctx context.Context) error {
	r.workWaiting = waiting.NewWorkRounds(r.db, r.work)
	// Recovery is synchronous: a client can never answer an overdue row as if
	// Core's downtime extended the deadline. Work hooks commit in the same write.
	if err := r.workWaiting.Expire(ctx, time.Now()); err != nil {
		return err
	}
	r.chainRefresh(func(ctx context.Context, topic string) error {
		if topic == "waiting" {
			return r.publishWorkQuestions(ctx)
		}
		return nil
	})
	watch, cancel := context.WithCancel(ctx)
	r.waitingStop, r.waitingDone = cancel, make(chan struct{})
	go func() { defer close(r.waitingDone); r.workWaiting.Run(watch, r.publishWorkQuestions) }()
	return nil
}
func (r *conversationRuntime) publishWorkQuestions(ctx context.Context) error {
	records, err := r.workWaiting.List(ctx, r.person)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := r.decisions.PublishQuestion(ctx, "work:"+record.ID); err != nil {
			return err
		}
	}
	return nil
}
func (q runQuestions) workQuestion(ctx context.Context, person, id string) (protocol.Question, error) {
	record, err := q.runtime.workWaiting.Get(ctx, id)
	if err != nil {
		return protocol.Question{}, err
	}
	if record.Person != person {
		return protocol.Question{}, waiting.ErrPerson
	}
	card := &protocol.DecisionCard{ID: "work:" + id, Revision: "1", Agent: q.runtime.decisions.AgentName(record.Agent), Summary: "Decisions for " + record.Goal, Targets: []protocol.DecisionTarget{{Name: "Deadline", Value: record.Deadline.Format(time.RFC3339Nano)}}}
	form := &protocol.QuestionForm{PluginName: "Work", Fields: []protocol.FormField{}}
	recommendations := []string{}
	for _, question := range record.Questions {
		recommendations = append(recommendations, question.Recommendation)
		choices := question.Choices
		form.Fields = append(form.Fields, protocol.FormField{Name: question.ID, Type: protocol.FormFieldTypeChoice, Choices: &choices})
		card.Targets = append(card.Targets, protocol.DecisionTarget{Name: question.ID, Value: question.Prompt}, protocol.DecisionTarget{Name: question.ID + " default", Value: question.Default})
	}
	recommendation := strings.Join(recommendations, "; ")
	card.Recommendation = &recommendation
	if len(record.Decisions) > 0 {
		last := record.Decisions[len(record.Decisions)-1]
		if last.Kind == "answer" || last.Kind == "late_answer" {
			outcome := protocol.DecisionOutcomeResolved
			card.Outcome = &outcome
		} else {
			// Retain the form after expiry: a later message creates a new decision.
			card.Targets = append(card.Targets, protocol.DecisionTarget{Name: "Status", Value: "Default applied; a new answer revises the goal"})
		}
	}
	return protocol.Question{ConversationID: record.Conversation, Card: card, Form: form}, nil
}
func (q runQuestions) AnswerQuestionMessage(ctx context.Context, person string, a protocol.QuestionAnswer, messageID string) (protocol.DecisionOutcome, error) {
	if a.CardID == nil || !strings.HasPrefix(*a.CardID, "work:") {
		return q.AnswerQuestion(ctx, person, a)
	}
	if a.Revision == nil || *a.Revision != "1" {
		return protocol.DecisionOutcomeStaleGroup, nil
	}
	// Declining a round is not permission to select arbitrary defaults before
	// their deadline. The person can explicitly choose do_not_act per effect.
	if a.Answer != protocol.QuestionAnswerActionAccept || a.Values == nil {
		return protocol.DecisionOutcomeRefused, nil
	}
	values := map[string]string{}
	for id, value := range *a.Values {
		if value.String == nil {
			return protocol.DecisionOutcomeRefused, nil
		}
		values[id] = *value.String
	}
	message, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	err = q.runtime.workWaiting.Answer(ctx, person, strings.TrimPrefix(*a.CardID, "work:"), waiting.RoundAnswer{MessageID: messageID, Message: string(message), Values: values}, time.Now())
	if err != nil {
		return "", err
	}
	return protocol.DecisionOutcomeResolved, nil
}
