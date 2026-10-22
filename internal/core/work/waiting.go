// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func waitKey(w Wait) string { return eventKey(w.Round, w.Question) }
func eventKey(parts ...string) string {
	b, _ := json.Marshal(parts)
	return fmt.Sprintf("waiting:%x", sha256.Sum256(b))
}
func hasWait(c Card) bool {
	for _, w := range c.Waits {
		if w.Held {
			return true
		}
	}
	return false
}
func taskCard(ctx context.Context, tx *canon.Tx, id string) (Card, error) {
	events, err := ledger(ctx, tx, id)
	if err != nil {
		return Card{}, err
	}
	return Project(events)
}

// OpenQuestionRound installs dependency holds in waiting's canon transaction.
func (s *Service) OpenQuestionRound(ctx context.Context, tx *canon.Tx, r QuestionRound) error {
	goal, err := taskCard(ctx, tx, r.Goal)
	if err != nil {
		return err
	}
	if goal.Task.PersonID != r.Person || goal.Task.AgentID != r.Agent || goal.Task.ConversationID != r.Conversation || goal.State == "stopped" {
		return ErrState
	}
	for _, q := range r.Questions {
		for _, id := range q.Dependents {
			c, err := taskCard(ctx, tx, id)
			if err != nil {
				return err
			}
			if c.Task.GoalID != r.Goal || c.Task.PersonID != r.Person {
				return ErrState
			}
			w := Wait{Round: r.ID, Question: q.ID, Held: true}
			if err = appendEvent(ctx, tx, Event{Key: eventKey("open", r.ID, q.ID, id), TaskID: id, Kind: "waiting_opened", Wait: &w}); err != nil {
				return err
			}
		}
	}
	return nil
}

// DecideQuestionRound is called by waiting in the round's canon transaction.
func (s *Service) DecideQuestionRound(ctx context.Context, tx *canon.Tx, r QuestionRound, d QuestionDecision) error {
	goal, err := taskCard(ctx, tx, r.Goal)
	if err != nil {
		return err
	}
	stopped := goal.State == "stopped"
	if err := appendEvent(ctx, tx, Event{Key: eventKey("decision", r.ID, d.MessageID), TaskID: r.Goal, Kind: "goal_decided", Decision: &GoalDecision{Round: r.ID, Decision: d}}); err != nil {
		return err
	}
	// Late answers revise the goal, never rewrite inputs of old executions or
	// resurrect an already applied effect. The manager derives revised tasks.
	if d.Kind == "late_answer" || stopped {
		return nil
	}
	for _, q := range r.Questions {
		for _, id := range q.Dependents {
			w := Wait{Round: r.ID, Question: q.ID, Value: d.Values[q.ID], Held: d.Values[q.ID] == DoNotAct}
			if err := appendEvent(ctx, tx, Event{Key: eventKey("resolve", r.ID, q.ID, id), TaskID: id, Kind: "waiting_decided", Wait: &w}); err != nil {
				return err
			}
		}
	}
	return nil
}
