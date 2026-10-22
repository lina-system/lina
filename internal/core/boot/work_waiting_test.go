// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

func workQuestionRuntime(t *testing.T) *conversationRuntime {
	t.Helper()
	r := controlRuntime(t)
	// Drive publication and expiry explicitly; no timer races in these checks.
	r.waitingStop()
	<-r.waitingDone
	return r
}

func openWorkQuestion(t *testing.T, r *conversationRuntime, id string) waiting.Round {
	t.Helper()
	goal := work.Task{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Report", Done: "Report exists"}
	if err := r.work.Create(t.Context(), goal); err != nil {
		t.Fatal(err)
	}
	child := goal
	child.ID, child.GoalID = id+":draft", id
	if err := r.work.Create(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	round := waiting.Round{ID: id, Goal: id, Person: r.person, Agent: r.agent, Conversation: r.conversation, Deadline: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), Questions: []waiting.Question{{ID: "route", Prompt: "Which route?", Recommendation: "A", Reason: waiting.SignificantChoice, Choices: []string{"A", "B"}, Default: "A", Dependents: []string{child.ID}}}}
	if err := r.workWaiting.Open(t.Context(), round); err != nil {
		t.Fatal(err)
	}
	return round
}

func workQuestionSnapshot(t *testing.T, r *conversationRuntime) clientsrv.TopicSnapshot {
	t.Helper()
	if err := r.identity.RefreshTopic(t.Context(), "waiting"); err != nil {
		t.Fatal(err)
	}
	start, sub, err := r.topics.Subscribe("waiting", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()
	return *start.Snapshot
}

func TestWorkQuestionsSkipUnchangedPublication(t *testing.T) {
	r := workQuestionRuntime(t)
	first := openWorkQuestion(t, r, "first")
	before := workQuestionSnapshot(t, r)
	if len(before.Items) != 1 {
		t.Fatalf("initial questions: %+v", before)
	}
	if again := workQuestionSnapshot(t, r); again.Cursor != before.Cursor {
		t.Errorf("unchanged refresh republished question: cursor %v -> %v", before.Cursor, again.Cursor)
	}
	openWorkQuestion(t, r, "second")
	after := workQuestionSnapshot(t, r)
	if len(after.Items) != 2 || after.Items[0].Revision != before.Items[0].Revision {
		t.Errorf("unrelated round republished editing form: before=%+v after=%+v", before, after)
	}
	if err := r.workWaiting.Expire(t.Context(), first.Deadline); err != nil {
		t.Fatal(err)
	}
	expired := workQuestionSnapshot(t, r)
	if expired.Items[0].Revision <= after.Items[0].Revision {
		t.Fatal("expiry did not update the question")
	}
	if again := workQuestionSnapshot(t, r); again.Cursor != expired.Cursor {
		t.Error("unchanged expired questions were republished")
	}
}

func TestWorkQuestionAnswerResolvesCard(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "timely"
		if late {
			name = "late"
		}
		t.Run(name, func(t *testing.T) {
			r := workQuestionRuntime(t)
			round := openWorkQuestion(t, r, "goal")
			at := round.Deadline.Add(-time.Hour)
			if late {
				at = round.Deadline
				if err := r.workWaiting.Expire(t.Context(), at); err != nil {
					t.Fatal(err)
				}
			}
			before := workQuestionSnapshot(t, r)
			var question protocol.Question
			if err := json.Unmarshal(before.Items[0].Data, &question); err != nil {
				t.Fatal(err)
			}
			if question.Card.Outcome != nil {
				t.Fatal("unanswered round is not answerable")
			}
			answer := waiting.RoundAnswer{MessageID: "answer", Message: "Use B", Values: map[string]string{"route": "B"}}
			if err := r.workWaiting.Answer(t.Context(), r.person, round.ID, answer, at); err != nil {
				t.Fatal(err)
			}
			after := workQuestionSnapshot(t, r)
			if err := json.Unmarshal(after.Items[0].Data, &question); err != nil {
				t.Fatal(err)
			}
			if question.Card.Outcome == nil || *question.Card.Outcome != protocol.DecisionOutcomeResolved {
				t.Fatalf("accepted %s answer left card open: %+v", name, question.Card)
			}
			if after.Items[0].Revision <= before.Items[0].Revision {
				t.Fatal("resolution not published")
			}
		})
	}
}
