// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"strings"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) a2aQuestion(ctx context.Context, person, id string) (protocol.Question, error) {
	calls, err := r.remoteAgents.Calls(ctx, person)
	if err != nil {
		return protocol.Question{}, err
	}
	for _, c := range calls {
		if "a2a:"+c.ID == id {
			card := &protocol.DecisionCard{ID: id, Revision: "1", Agent: r.decisions.AgentName(c.Origin.Agent), Summary: c.Question, Targets: []protocol.DecisionTarget{{Name: "Connection", Value: c.Connection}}}
			form := &protocol.QuestionForm{PluginName: "Remote agent", Fields: []protocol.FormField{{Name: "Answer", Type: protocol.FormFieldTypeText}}}
			if c.State == "auth_required" {
				card.Summary = "Sign in from the remote agent connection settings."
				form = nil
			}
			if c.Answer != "" || c.State != "input_required" && c.State != "auth_required" {
				outcome := protocol.DecisionOutcomeResolved
				card.Outcome = &outcome
			}
			conn, e := r.remoteAgents.Connection(ctx, person, c.Connection)
			if e != nil || !conn.Enabled || conn.Revision != c.Revision {
				outcome := protocol.DecisionOutcomeWithdrawn
				card.Outcome = &outcome
			}
			return protocol.Question{ConversationID: c.Origin.Conversation, Card: card, Form: form}, nil
		}
	}
	return protocol.Question{}, a2a.ErrScope
}
func (r *conversationRuntime) answerA2AQuestion(ctx context.Context, person string, p protocol.QuestionAnswer) (protocol.DecisionOutcome, error) {
	if p.CardID == nil || p.Revision == nil || *p.Revision != "1" {
		return protocol.DecisionOutcomeStaleGroup, nil
	}
	q, err := r.a2aQuestion(ctx, person, *p.CardID)
	if err != nil {
		return protocol.DecisionOutcomeRefused, err
	}
	if q.Card.Outcome != nil {
		return *q.Card.Outcome, nil
	}
	if p.Answer != protocol.QuestionAnswerActionAccept || p.Values == nil || len(*p.Values) != 1 {
		return protocol.DecisionOutcomeRefused, nil
	}
	v := (*p.Values)["Answer"]
	if v.String == nil {
		return protocol.DecisionOutcomeRefused, nil
	}
	calls, err := r.remoteAgents.Calls(ctx, person)
	if err != nil {
		return protocol.DecisionOutcomeRefused, err
	}
	for _, c := range calls {
		if c.ID == strings.TrimPrefix(*p.CardID, "a2a:") {
			if err = r.remoteAgents.Answer(ctx, c.Origin, c.ID, *v.String); err != nil {
				return protocol.DecisionOutcomeRefused, err
			}
			return protocol.DecisionOutcomeResolved, nil
		}
	}
	return protocol.DecisionOutcomeRefused, a2a.ErrScope
}
func (r *conversationRuntime) restoreA2AQuestions(ctx context.Context) error {
	if r.remoteAgents == nil {
		return nil
	}
	calls, err := r.remoteAgents.Calls(ctx, r.person)
	if err != nil {
		return err
	}
	for _, c := range calls {
		if (c.State == "input_required" || c.State == "auth_required") && c.Answer == "" {
			if err = r.decisions.PublishQuestion(ctx, "a2a:"+c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

type a2aWaitingSource struct {
	r     *conversationRuntime
	names map[string]string
}

func (s a2aWaitingSource) Snapshot(ctx context.Context, person string) ([]waiting.LiveItem, error) {
	out := []waiting.LiveItem{}
	if s.r.remoteAgents == nil {
		return out, nil
	}
	calls, err := s.r.remoteAgents.Calls(ctx, person)
	if err != nil {
		return nil, err
	}
	for _, c := range calls {
		if (c.State != "input_required" && c.State != "auth_required" && c.State != "unknown" && c.State != "cancellation_unknown") || c.Answer != "" {
			continue
		}
		sentence := c.Question
		if sentence == "" {
			sentence = "The remote outcome is unknown. Inspect the existing call before sending more work."
		}
		out = append(out, waiting.LiveItem{ID: "a2a:" + c.ID, Item: waiting.Item{Person: person, Agent: c.Origin.Agent, Revision: 1, Reason: waiting.MissingInformation, Blocking: true, Requested: c.Deadline.Add(-10 * 60 * 1000000000), View: view.Waiting{Kind: view.Reply, Sentence: sentence, Context: c.Connection, AgentName: s.names[c.Origin.Agent], Answers: []string{"open"}, Effort: view.FiveMinutes}}})
	}
	return out, nil
}
