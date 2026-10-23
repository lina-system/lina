// SPDX-License-Identifier: Apache-2.0
package waiting

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
)

// Answers use the authenticated question card, just like work-round questions.
type ConversationQuestionSource struct{ AgentName func(string) string }

func (ConversationQuestionSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	return roundIDs(ctx, tx, `SELECT q.id FROM conversation_questions q WHERE person=? AND NOT EXISTS(SELECT 1 FROM conversation_question_resolutions r WHERE r.question=q.id) ORDER BY q.rowid`, person)
}
func (s ConversationQuestionSource) Read(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	q, err := readConversationQuestion(ctx, tx, id)
	if err != nil {
		return Item{}, err
	}
	visible, err := ConversationQuestionVisible(ctx, tx, q)
	if err != nil {
		return Item{}, err
	}
	name := ""
	if s.AgentName != nil {
		name = s.AgentName(q.Agent)
	}
	return Item{Person: q.Person, Agent: q.Agent, Revision: 1, Reason: MissingInformation, Requested: q.AskedAt, Finished: !visible || q.Resolution != nil || s.AgentName != nil && name == "", View: view.Waiting{Kind: view.Reply, Sentence: q.Question, Recommendation: q.Recommendation, AgentName: name, Answers: []string{"open"}, Effort: view.FiveMinutes}}, nil
}
func (ConversationQuestionSource) Done(context.Context, *canon.Tx, string) error { return nil }

// Retraction keeps authorized display history, but withdraws actionable copies.
// This also runs inside the answer transaction, so forget and answer serialize.
func ConversationQuestionVisible(ctx context.Context, tx *canon.Tx, q ConversationQuestion) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM retract_visible_event WHERE id=? AND agent_id=?", q.Input, q.Agent).Scan(&count)
	return count == 1, err
}
