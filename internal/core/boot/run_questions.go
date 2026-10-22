// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/agents"
	"strings"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
)

type runQuestions struct{ runtime *conversationRuntime }

func (q runQuestions) Question(ctx context.Context, person, id string) (protocol.Question, error) {
	r := q.runtime
	if r.background == nil || !strings.HasSuffix(id, ":effects") {
		return protocol.Question{}, runs.ErrState
	}
	record, err := r.background.service.Get(ctx, strings.TrimSuffix(id, ":effects"))
	if err != nil {
		return protocol.Question{}, err
	}
	if person != record.Origin.Person {
		return protocol.Question{}, runs.ErrScope
	}
	if record.EffectsQuestion == nil {
		return protocol.Question{}, runs.ErrState
	}
	delivered, err := r.background.notices.Delivered(ctx, record.Origin.Conversation)
	if err != nil {
		return protocol.Question{}, err
	}
	found := false
	for _, d := range delivered {
		for _, n := range d.Notices {
			found = found || n.ID == id
		}
	}
	if !found {
		return protocol.Question{}, runs.ErrState
	}
	card := &protocol.DecisionCard{ID: id, Revision: "1", Agent: r.decisions.AgentName(record.Origin.Agent), Summary: runSummary(runs.Notice{EffectQuestion: true}), Targets: []protocol.DecisionTarget{{Name: "Goal", Value: record.Brief.Goal}}}
	if record.EffectsQuestion.Answer != nil {
		outcome := protocol.DecisionOutcomeResolved
		card.Outcome = &outcome
	}
	if _, err := r.agents.Get(ctx, record.Origin.Agent); err != nil {
		if !errors.Is(err, agents.ErrNotFound) {
			return protocol.Question{}, err
		}
		outcome := protocol.DecisionOutcomeWithdrawn
		card.Outcome = &outcome
	}
	return protocol.Question{ConversationID: record.Origin.Conversation, Card: card, Form: &protocol.QuestionForm{PluginName: "Background work", Fields: []protocol.FormField{{Name: "Direction", Type: protocol.FormFieldTypeText}}}}, nil
}

func (q runQuestions) AnswerQuestion(ctx context.Context, person string, answer protocol.QuestionAnswer) (protocol.DecisionOutcome, error) {
	if answer.CardID == nil || answer.Revision == nil || *answer.Revision != "1" {
		return protocol.DecisionOutcomeStaleGroup, nil
	}
	question, err := q.Question(ctx, person, *answer.CardID)
	if err != nil {
		return "", err
	}
	if question.Card.Outcome != nil {
		return *question.Card.Outcome, nil
	}
	text := ""
	if answer.Answer == protocol.QuestionAnswerActionAccept {
		if answer.Values == nil || len(*answer.Values) != 1 {
			return protocol.DecisionOutcomeRefused, nil
		}
		value := (*answer.Values)["Direction"]
		if value.String == nil {
			return protocol.DecisionOutcomeRefused, nil
		}
		text = *value.String
	}
	err = q.runtime.background.service.AnswerEffectQuestion(ctx, person, strings.TrimSuffix(*answer.CardID, ":effects"), string(answer.Answer), text)
	if err != nil {
		return "", err
	}
	return protocol.DecisionOutcomeResolved, nil
}

func (r *conversationRuntime) publishRunQuestions(ctx context.Context, notices []lane.Notice) error {
	for _, notice := range notices {
		if notice.ID != notice.Run+":effects" {
			continue
		}
		if err := r.router.root.decisions.PublishQuestion(ctx, notice.ID); err != nil {
			return err
		}
	}
	return nil
}

// A fresh waiting subscription restores only questions whose lane delivery has
// committed. A question queued during an active turn cannot surface early.
func (r *conversationRuntime) restoreRunQuestions(ctx context.Context) error {
	if r.background == nil {
		return nil
	}
	records, err := r.runRecords(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, record := range records {
		conversation := record.Origin.Conversation
		if record.Origin.Person != r.person || seen[conversation] {
			continue
		}
		seen[conversation] = true
		deliveries, err := r.background.notices.Delivered(ctx, conversation)
		if err != nil {
			return err
		}
		for _, d := range deliveries {
			if err = r.publishRunQuestions(ctx, d.Notices); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *conversationRuntime) deliverEffectAnswer(ctx context.Context, record runs.Record) error {
	if record.EffectsQuestion == nil || record.EffectsQuestion.Answer == nil || record.EffectsQuestion.Answer.Delivered {
		return nil
	}
	answer := record.EffectsQuestion.Answer
	view, err := r.router.view(ctx, clientsrv.Session{PersonID: r.person}, record.Origin.Agent)
	if err != nil {
		return err
	}
	id := "run-effect-answer:" + record.ID
	_, err = view.Input(ctx, clientsrv.Session{PersonID: answer.Person}, protocol.Input{ConversationID: &record.Origin.Conversation, InputID: &id, Text: &answer.Text})
	if err != nil {
		return err
	}
	return r.background.service.AcknowledgeEffectAnswer(ctx, record.ID)
}
