// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/loop"
)

func questionChannelInputs(t *testing.T, r *conversationRuntime) func(string, string) (*conversationRuntime, ledger.Event) {
	t.Helper()
	ctx, now := t.Context(), time.Now()
	bot := channels.Bot{Platform: "discord", Account: "fixture"}
	store := r.channels.store
	if err := store.Connect(ctx, bot, r.person); err != nil {
		t.Fatal(err)
	}
	code, err := store.PairingCode(ctx, bot, r.person, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Pair(ctx, bot, code, "42", "100", now); err != nil {
		t.Fatal(err)
	}
	previous := ""
	return func(topic, native string) (*conversationRuntime, ledger.Event) {
		t.Helper()
		binding := channels.Binding{Address: channels.Address{Bot: bot, Chat: "group", Topic: topic}, Agent: r.agent, Person: r.person, Conversation: r.conversation, Scope: channels.Scope{Reads: channels.Addressed, Writes: channels.ReplyOnly, Tools: true, Memory: false}}
		if err := store.Bind(ctx, binding); err != nil {
			t.Fatal(err)
		}
		var delivery gateway.Delivery
		if err := gateway.New(r.db, func(_ context.Context, d gateway.Delivery) error { delivery = d; return nil }).Admit(ctx, gateway.Inbound{Address: binding.Address, NativeID: native, Sender: "42", PreviousCursor: previous, Cursor: native, Addressed: true, Body: []byte(`{"Text":"Prepare a draft"}`)}); err != nil {
			t.Fatal(err)
		}
		previous = native
		input, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "channel-input:" + strconv.FormatInt(delivery.InboxID, 10), ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "channel", Platform: bot.Platform, ChatID: binding.Chat}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Prepare a draft"})
		if err != nil {
			t.Fatal(err)
		}
		view, err := r.workerCompletionView(ctx, ledger.Event{ParentID: input.ID, AgentID: r.agent, ConversationID: r.conversation, Surface: input.Surface})
		if err != nil {
			t.Fatal(err)
		}
		return view, input
	}
}
func TestQuestionChannelScope(t *testing.T) {
	r := controlRuntime(t)
	method := agents.ProjectWorkMethod(agents.Preparation{}, agents.State{})
	ask := func(view *conversationRuntime, input ledger.Event, label string) {
		t.Helper()
		b, _ := questionBindings(t, view, input, method)
		result := questionCall(t, b, "ask_question", label, waiting.QuestionDraft{Question: label, Recommendation: label, Default: label})
		if result.Code != loop.OK {
			t.Fatal(result)
		}
	}
	ask(r, questionPersonInput(t, r, "private", "Private draft"), "app-private")
	channel := questionChannelInputs(t, r)
	view, first := channel("one", "first")
	ask(view, first, "own-thread")
	other, second := channel("two", "second")
	ask(other, second, "other-thread")
	view, next := channel("one", "next")
	gate := &questionTurn{}
	item, err := view.prepareQuestionTool(t.Context(), "next-turn", next, method, map[string]loop.Tool{}, gate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(item.Text, "app-private") || strings.Contains(item.Text, "other-thread") || !strings.Contains(item.Text, "own-thread") || len(gate.pending) != 1 {
		t.Fatalf("channel received unauthorized questions: %s", item.Text)
	}
	view.channelBinding.Scope.Tools = false
	_, gate = questionBindings(t, view, next, method)
	if err := gate.check(t.Context()); err != nil {
		t.Fatalf("disabled question tools blocked the channel: %v", err)
	}
}
func TestQuestionChannelOriginChain(t *testing.T) {
	r := controlRuntime(t)
	channel := questionChannelInputs(t, r)
	view, input := channel("one", "first")
	for i, kind := range []string{"worker_completion", "question_answer", "question_answer"} {
		at := time.Now()
		next, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: kind + ":" + strconv.Itoa(i), ParentID: input.ID, ConversationID: r.conversation, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: kind, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "Recorded answer"})
		if err != nil {
			t.Fatal(err)
		}
		input = next
	}
	// This is the same event shape committed by a card answer and recovered at boot.
	restored, err := r.workerCompletionView(t.Context(), ledger.Event{ParentID: input.ID, ConversationID: r.conversation, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: "question_answer"})
	if err != nil {
		t.Fatalf("question continuation lost its channel: %v", err)
	}
	if restored.channelAddress != view.channelAddress || *restored.channelBinding != *view.channelBinding {
		t.Fatal("question continuation changed scope")
	}
	if err = r.channels.store.Disconnect(t.Context(), view.channelAddress.Bot, r.person); err != nil {
		t.Fatal(err)
	}
	if _, err = r.workerCompletionView(t.Context(), ledger.Event{ParentID: input.ID, ConversationID: r.conversation, AgentID: r.agent, Surface: input.Surface}); err == nil {
		t.Fatal("revoked channel continuation admitted")
	}
}
