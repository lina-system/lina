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
	"github.com/thisisjun786/lina/kit/provider"
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
			if !sameTurn {
				if got := questionCall(t, bindings, "resolve_question", "related-again", map[string]string{"id": question.ID, "kind": "related", "quote": input.Body.Text()}); got.Code != loop.OK {
					t.Fatal(got)
				}
				if err := gate.check(t.Context()); err == nil {
					t.Error("earlier related assessment cleared the new answer gate")
				}
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
	if err := gate.check(t.Context()); err == nil {
		t.Error("earlier related assessment allowed completion before the topic change was assessed")
	}
	if got := gate.before(t.Context(), provider.ToolCall{ID: "work", Name: "write_record"}); got == nil || got.Code != loop.Invalid {
		t.Errorf("earlier related assessment allowed work: %+v", got)
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
	if err := gate.check(t.Context()); err != nil {
		t.Fatal("default resolution did not release the gate", err)
	}
}

func TestQuestionSteeringRelatedInputs(t *testing.T) {
	r := controlRuntime(t)
	input := questionPersonInput(t, r, "ask", "Prepare a draft")
	bindings, gate := questionBindings(t, r, input, agents.ProjectWorkMethod(agents.Preparation{}, agents.State{}))
	result := questionCall(t, bindings, "ask_question", "format", waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Default: "table"})
	var question waiting.ConversationQuestion
	if result.Code != loop.OK || json.Unmarshal(result.Output, &question) != nil {
		t.Fatal(result)
	}
	first := questionPersonInput(t, r, "first", "Why a table? Is it easier to compare?")
	last := questionPersonInput(t, r, "last", "Why a table? Does it fit on one page?")
	for _, event := range []ledger.Event{first, last} {
		if err := gate.consume(t.Context(), acceptedSteering{event: event, runner: r}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"earlier", "retry"} {
		if got := questionCall(t, bindings, "resolve_question", id, map[string]string{"id": question.ID, "kind": "related", "quote": first.Body.Text()}); got.Code != loop.OK {
			t.Fatal(got)
		}
		if err := gate.check(t.Context()); err == nil {
			t.Error("earlier related assessment cleared the latest input gate")
		}
	}
	// A shared quote selects the latest input. Once assessed, reprocessing an
	// earlier input must not erase that assessment and block work again.
	if got := questionCall(t, bindings, "resolve_question", "latest", map[string]string{"id": question.ID, "kind": "related", "quote": "Why a table?"}); got.Code != loop.OK {
		t.Fatal(got)
	}
	if got := questionCall(t, bindings, "resolve_question", "earlier-again", map[string]string{"id": question.ID, "kind": "related", "quote": first.Body.Text()}); got.Code != loop.OK {
		t.Fatal(got)
	}
	if err := gate.check(t.Context()); err != nil {
		t.Fatal("latest related input assessed but gate remained blocked", err)
	}
	// Identical text arriving as a new event still needs a fresh assessment.
	repeated := questionPersonInput(t, r, "repeated", last.Body.Text())
	if err := gate.consume(t.Context(), acceptedSteering{event: repeated, runner: r}); err != nil {
		t.Fatal(err)
	}
	if err := gate.check(t.Context()); err == nil {
		t.Error("new event with repeated text inherited an older assessment")
	}
	if got := questionCall(t, bindings, "resolve_question", "repeated", map[string]string{"id": question.ID, "kind": "related", "quote": repeated.Body.Text()}); got.Code != loop.OK {
		t.Fatal(got)
	}
	if err := gate.check(t.Context()); err != nil {
		t.Fatal("repeated text could not be assessed", err)
	}
	stored, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), question.ID)
	if err != nil || stored.Resolution != nil {
		t.Fatalf("related assessments closed the question: %+v %v", stored, err)
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
