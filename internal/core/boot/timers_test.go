// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/timers"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestTimerToolsBindConversation(t *testing.T) {
	r := controlRuntime(t)
	bindings := map[string]loop.Tool{}
	if len(r.prepareTimerTools("turn", bindings)) != 3 {
		t.Fatal("missing timer tools")
	}
	call := provider.ToolCall{ID: "call", Name: "create_timer", Arguments: `{"schedule":{"at":"2099-01-01T00:00:00Z"},"action":{"kind":"reminder","text":"Review notes"}}`}
	result := bindings[call.Name].Execute(t.Context(), call)
	if result.Code != loop.OK {
		t.Fatalf("create: %+v", result)
	}
	var timer timers.Timer
	if err := json.Unmarshal(result.Output, &timer); err != nil {
		t.Fatal(err)
	}
	if timer.Person != r.person || timer.Agent != r.agent || timer.Conversation != r.conversation {
		t.Fatalf("unbound timer=%+v", timer)
	}
	replay := bindings[call.Name].Execute(t.Context(), call)
	if replay.Code != loop.OK || string(replay.Output) != string(result.Output) {
		t.Fatalf("replay=%+v", replay)
	}
	call.ID = "spoof"
	call.Arguments = `{"person":"another","schedule":{"at":"2099-01-01T00:00:00Z"},"action":{"kind":"reminder","text":"Review notes"}}`
	if got := bindings[call.Name].Execute(t.Context(), call); got.Code != loop.Invalid {
		t.Fatal("accepted model ownership", got)
	}
	foreign := newConversationView(r, "other-agent", "main:other-agent")
	other := map[string]loop.Tool{}
	foreign.prepareTimerTools("other-turn", other)
	raw, _ := json.Marshal(timerRef{ID: timer.ID})
	for _, name := range []string{"timer_status", "cancel_timer"} {
		got := other[name].Execute(t.Context(), provider.ToolCall{ID: name, Name: name, Arguments: string(raw)})
		if got.Code == loop.OK {
			t.Fatal("foreign access", name)
		}
	}
	foreign.channelBinding = &channels.Binding{}
	if declarations := foreign.prepareTimerTools("channel", map[string]loop.Tool{}); len(declarations) != 0 {
		t.Fatal("delayed channel authority was not bound")
	}
}

func TestTimerWorkContext(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	e, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: "timer-work", ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "timer_work", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Saved work request"})
	if err != nil {
		t.Fatal(err)
	}
	items, _, _, events, err := r.modelContext(t.Context(), e.ID)
	if err != nil || len(items) != 1 || len(events) != 1 || events[0].ID != e.ID {
		t.Fatalf("model input lost: %v %+v %v", items, events, err)
	}
	display, err := r.historyItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range display {
		if item.eventID == e.ID {
			t.Fatal("internal work scaffold displayed")
		}
	}
}
