// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/loop"
)

func TestQuestionLiveSteering(t *testing.T) {
	for _, sameTurn := range []bool{false, true} {
		name := "older-question"
		if sameTurn {
			name = "same-turn-question"
		}
		t.Run(name, func(t *testing.T) {
			r := controlRuntime(t)
			method := agents.ProjectWorkMethod(agents.Preparation{}, agents.State{})
			input := questionPersonInput(t, r, "ask", "Prepare a draft")
			bindings, gate := questionBindings(t, r, input, method)
			result := questionCall(t, bindings, "ask_question", "format", waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Choices: []string{"table", "list"}, Default: "table"})
			var question waiting.ConversationQuestion
			if result.Code != loop.OK || json.Unmarshal(result.Output, &question) != nil {
				t.Fatal(result)
			}
			if !sameTurn {
				input = questionPersonInput(t, r, "related", "Why a table?")
				bindings, gate = questionBindings(t, r, input, method)
				if result := questionCall(t, bindings, "resolve_question", "related", map[string]string{"id": question.ID, "kind": "related", "quote": input.Body.Text()}); result.Code != loop.OK {
					t.Fatal(result)
				}
			}
			answer := questionPersonInput(t, r, "answer", "Use a list")
			items, err := assemble.History([]ledger.Event{answer}, r.conversation, r.agent)
			if err != nil || len(items) != 1 {
				t.Fatalf("steering projection: %v %v", items, err)
			}
			r.steering = map[string][]acceptedSteering{string(items[0]): {{event: answer, runner: r}}}
			inputs := &loop.Inputs{}
			inputs.Push(items[0])
			args := map[string]string{"id": question.ID, "kind": "answered", "quote": answer.Body.Text(), "value": "list"}
			if got := questionCall(t, bindings, "resolve_question", "before-drain", args); got.Code != loop.Invalid {
				t.Fatal("unconsumed steering authorized resolution", got)
			}
			queue := &conversationInputs{runtime: r, inputs: inputs, questions: gate}
			if drained := queue.Drain(); len(drained) != 1 || queue.failure != nil {
				t.Fatalf("drain=%v error=%v", drained, queue.failure)
			}
			if err := gate.check(t.Context()); err == nil {
				t.Error("consumed answer did not require a new question assessment")
			}
			for _, id := range []string{"answer", "retry"} {
				if got := questionCall(t, bindings, "resolve_question", id, args); got.Code != loop.OK {
					t.Fatalf("consumed owner answer refused: %+v", got)
				}
			}
			stored, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), question.ID)
			if err != nil || stored.Resolution == nil || stored.Resolution.Kind != "answered" || stored.Resolution.Value != "list" || stored.Resolution.Input != answer.ID {
				t.Fatalf("answer receipt: %+v %v", stored, err)
			}
			if err := gate.check(t.Context()); err != nil {
				t.Fatal(err)
			}
			card, err := r.conversationQuestionCard(t.Context(), r.person, question.ID)
			if err != nil || card.Card.Outcome == nil {
				t.Fatalf("answered card remained open: %+v %v", card, err)
			}
			_, next := questionBindings(t, r, questionPersonInput(t, r, "next", "Tell me the time"), method)
			if len(next.pending) != 0 {
				t.Fatal("answered question can later apply a contradictory default")
			}
		})
	}
}

func TestQuestionSteeringTrust(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ledger.Event)
	}{
		{"forwarded", func(e *ledger.Event) { e.SpeakerKind, e.Origin.Entry = guard.Forwarded, guard.EntryForwarded }},
		{"system", func(e *ledger.Event) { e.SpeakerKind, e.Origin.Entry = guard.System, guard.EntrySystem }},
		{"other-person", func(e *ledger.Event) { e.PersonID = "other" }},
		{"other-agent", func(e *ledger.Event) { e.AgentID = "other" }},
		{"other-conversation", func(e *ledger.Event) { e.ConversationID = "other" }},
		{"imported", func(e *ledger.Event) { e.Origin.Kind = "import" }},
		{"replayed", func(e *ledger.Event) { e.ReplayOf = "source" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := controlRuntime(t)
			input := questionPersonInput(t, r, "ask", "Prepare a draft")
			bindings, gate := questionBindings(t, r, input, agents.ProjectWorkMethod(agents.Preparation{}, agents.State{}))
			result := questionCall(t, bindings, "ask_question", "format", waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Default: "table"})
			var question waiting.ConversationQuestion
			if result.Code != loop.OK || json.Unmarshal(result.Output, &question) != nil {
				t.Fatal(result)
			}
			answer := questionPersonInput(t, r, "answer", "Use a list")
			tc.change(&answer)
			if err := gate.consume(t.Context(), acceptedSteering{event: answer, runner: r}); err != nil {
				t.Fatal(err)
			}
			if got := questionCall(t, bindings, "resolve_question", "answer", map[string]string{"id": question.ID, "kind": "answered", "quote": answer.Body.Text(), "value": "list"}); got.Code != loop.Invalid {
				t.Fatal("untrusted input resolved question", got)
			}
			if err := gate.check(t.Context()); err != nil {
				t.Fatal("untrusted input made new question mandatory", err)
			}
		})
	}
}

func TestQuestionSteeringDefaultSource(t *testing.T) {
	r := controlRuntime(t)
	input := questionPersonInput(t, r, "ask", "Prepare a draft")
	bindings, gate := questionBindings(t, r, input, agents.ProjectWorkMethod(agents.Preparation{}, agents.State{}))
	result := questionCall(t, bindings, "ask_question", "format", waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Default: "table"})
	var question waiting.ConversationQuestion
	if result.Code != loop.OK || json.Unmarshal(result.Output, &question) != nil {
		t.Fatal(result)
	}
	// Neither the question's original input nor steering received before it was
	// asked may pass it over. Only subsequently consumed input is eligible.
	if got := questionCall(t, bindings, "resolve_question", "early", map[string]string{"id": question.ID, "kind": "default_applied", "quote": input.Body.Text()}); got.Code != loop.Invalid {
		t.Fatal("question passed over by its own input", got)
	}
	first := questionPersonInput(t, r, "related", "Why a table?")
	next := questionPersonInput(t, r, "unrelated", "Tell me the time")
	for _, input := range []ledger.Event{first, next} {
		if err := gate.consume(t.Context(), acceptedSteering{event: input, runner: r}); err != nil {
			t.Fatal(err)
		}
	}
	if got := questionCall(t, bindings, "resolve_question", "related", map[string]string{"id": question.ID, "kind": "related", "quote": first.Body.Text()}); got.Code != loop.OK {
		t.Fatal("earlier consumed source was lost", got)
	}
	for _, id := range []string{"default", "retry"} {
		if got := questionCall(t, bindings, "resolve_question", id, map[string]string{"id": question.ID, "kind": "default_applied", "quote": next.Body.Text()}); got.Code != loop.OK {
			t.Fatal(got)
		}
	}
	stored, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), question.ID)
	if err != nil || stored.Resolution == nil || stored.Resolution.Input != next.ID || stored.Resolution.Value != "table" {
		t.Fatalf("default receipt: %+v %v", stored, err)
	}
	notice, err := r.journal().Get(t.Context(), question.ID+":default:"+next.ID)
	if err != nil || notice.ParentID != next.ID {
		t.Fatalf("default notice source: %+v %v", notice, err)
	}
}

func TestQuestionSteeringKeepsChannelScope(t *testing.T) {
	r := controlRuntime(t)
	input := questionPersonInput(t, r, "private", "Prepare a private draft")
	method := agents.ProjectWorkMethod(agents.Preparation{}, agents.State{})
	bindings, gate := questionBindings(t, r, input, method)
	result := questionCall(t, bindings, "ask_question", "format", waiting.QuestionDraft{Question: "Private format?", Recommendation: "Use a table", Default: "table"})
	var question waiting.ConversationQuestion
	if result.Code != loop.OK || json.Unmarshal(result.Output, &question) != nil {
		t.Fatal(result)
	}
	channel := questionChannelInputs(t, r)
	view, answer := channel("one", "answer")
	if err := gate.consume(t.Context(), acceptedSteering{event: answer, runner: view}); err != nil {
		t.Fatal(err)
	}
	if got := questionCall(t, bindings, "resolve_question", "answer", map[string]string{"id": question.ID, "kind": "answered", "quote": answer.Body.Text(), "value": "list"}); got.Code != loop.Invalid {
		t.Fatal("restricted steering resolved a private question", got)
	}
	if err := gate.check(t.Context()); err != nil {
		t.Fatal("restricted steering made private question mandatory", err)
	}
}
