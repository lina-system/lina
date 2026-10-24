// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/compaction"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestLiveTopicRecall(t *testing.T) {
	for _, tc := range []struct {
		name, query   string
		barred, match bool
	}{
		{"match", "Tell me about observatory", false, true},
		{"no-match", "unrelated-zebras", false, false},
		{"long-unicode", "observatory " + strings.Repeat("나", 200), false, true},
		{"barred", "observatory", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := turnRuntime(t)
			conversation := r.conversation
			r.conversation = "earlier-topic"
			source := queuedContextEvent(t, r, "topic-source", "My observatory telescope is violet.", guard.EntryOwner, "")
			item, err := coreexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Person: r.person, Agent: r.agent, Item: memory.Item{
				Kind: memory.Fact, Content: source.Body.Text(), Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated,
				Evidence: []memory.Evidence{{Kind: memory.Event, ID: source.ID, Precision: memory.Exact}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			r.conversation = conversation
			if tc.barred {
				r.channelBinding = &channels.Binding{Scope: channels.Scope{Tools: true}}
			}
			input, _ := admitTurnInput(t, r, tc.query)
			s := turnState{runtime: r, input: input, turnID: "turn:" + input.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}}
			prepared, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(prepared.Request.Items)
			if strings.Contains(string(raw), "violet") != tc.match {
				t.Errorf("topic content present=%v, want %v", strings.Contains(string(raw), "violet"), tc.match)
			}
			found := false
			for _, ref := range prepared.Assembly.Context {
				if ref.ID == "memory:"+item.ID && ref.Revision == "1" {
					found = true
				}
			}
			if found != tc.match {
				t.Errorf("topic evidence=%v, want %v", found, tc.match)
			}
			for _, name := range []string{"recall", "expand"} {
				_, bound := s.bindings[name]
				declared := false
				for _, decl := range prepared.Request.Tools {
					if declarationName(decl) == name {
						declared = true
					}
				}
				if bound != !tc.barred || declared != !tc.barred {
					t.Errorf("%s bound=%v declared=%v barred=%v", name, bound, declared, tc.barred)
				}
			}
			if !tc.barred && s.bindings["recall"].Execute != nil {
				out := s.bindings["recall"].Execute(t.Context(), provider.ToolCall{ID: "read", Name: "recall", Arguments: `{"query":"observatory"}`})
				if out.Code != loop.OK || !strings.Contains(string(out.Output), item.ID) {
					t.Errorf("recall: %+v", out)
				}
			}
		})
	}
}

func TestLiveTopicReplay(t *testing.T) {
	r, p := turnRuntime(t)
	conversation := r.conversation
	r.conversation = "earlier"
	source := queuedContextEvent(t, r, "old-topic", "The observatory telescope is violet.", guard.EntryOwner, "")
	item, err := coreexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Person: r.person, Agent: r.agent, Item: memory.Item{Kind: memory.Fact, Content: source.Body.Text(), Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: source.ID, Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	r.conversation = conversation
	p.steps = []turnStep{{events: turnText("The telescope is violet.")}}
	input, inboxID := admitTurnInput(t, r, "observatory")
	if err := r.run(t.Context(), &loop.Inputs{}, input, inboxID); err != nil {
		t.Fatal(err)
	}
	ep, err := r.replaySource().Load(t.Context(), replay.Scope{Person: r.person, Agent: r.agent}, "turn:"+input.ID)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := replay.Request(ep, nil, "", "", replayInstructions)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt.Purpose = provider.Turn
	if got := assemble.Digest(rebuilt); got != ep.Evidence.Hash {
		t.Fatalf("digest=%s want %s", got, ep.Evidence.Hash)
	}
	found := false
	for _, ref := range ep.Evidence.Assembly.Context {
		if ref.ID == "memory:"+item.ID && ref.Source == "topic-memory-v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("episode omitted recalled revision")
	}
	if err := r.removals.Forget(t.Context(), retract.Request{Person: r.person, Targets: []retract.Ref{{Kind: retract.Memory, ID: item.ID}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.replaySource().ValidateEpisode(t.Context(), ep); err == nil {
		t.Fatal("forgotten topic replayed")
	}
}

func TestReplayRecallKeepsReplayVisibility(t *testing.T) {
	r, _ := turnRuntime(t)
	own := queuedContextEvent(t, r, "own-source", "The aurora plan is ready.", guard.EntryOwner, "")
	hidden := queuedContextEvent(t, r, "private-source", "The aurora private fact.", guard.EntryOwner, "")
	item, err := coreexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Person: r.person, Agent: r.agent, Item: memory.Item{Kind: memory.Fact, Content: "The aurora private memory.", Scope: memory.Private, Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: hidden.ID, Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	source := r.replaySource()
	epoch, err := source.Epoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reads, err := r.replayReads(source, replay.Episode{TurnID: "replayed", RevocationEpoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	out := reads["recall"].Execute(t.Context(), provider.ToolCall{ID: "search", Name: "recall", Arguments: `{"query":"aurora"}`})
	if out.Code != loop.OK || !strings.Contains(string(out.Output), own.ID) || strings.Contains(string(out.Output), hidden.ID) || strings.Contains(string(out.Output), item.ID) {
		t.Fatalf("replay recall: %+v", out)
	}
}

func TestLiveRecallScopeAndExpansion(t *testing.T) {
	r, _ := turnRuntime(t)
	own := queuedContextEvent(t, r, "own-source", "The aurora plan is ready.", guard.EntryOwner, "")
	reply := queuedContextEvent(t, r, "own-reply", "The aurora result.", guard.EntryAgent, "")
	hidden := queuedContextEvent(t, r, "hidden-source", "The aurora private fact.", guard.EntryOwner, "")
	_, err := coreexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Person: r.person, Agent: r.agent, Item: memory.Item{Kind: memory.Fact, Content: hidden.Body.Text(), Scope: memory.NeverLeaves, Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: hidden.ID, Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.agents.Create(t.Context(), agents.Create{Name: "Other", PersonID: r.person, Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	agent, conversation := r.agent, r.conversation
	r.agent, r.conversation = other.ID, "foreign-conversation"
	foreign := queuedContextEvent(t, r, "foreign-source", "The aurora foreign fact.", guard.EntryOwner, "")
	r.agent, r.conversation = agent, conversation
	bindings := map[string]loop.Tool{}
	if _, err := r.prepareRecallTools("scope-turn", nil, bindings); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		id      string
		allowed bool
	}{{own.ID, true}, {reply.ID, true}, {hidden.ID, false}, {foreign.ID, false}, {"missing", false}} {
		args, _ := json.Marshal(map[string]any{"id": "event:" + e.id})
		out := bindings["expand"].Execute(t.Context(), provider.ToolCall{ID: e.id, Name: "expand", Arguments: string(args)})
		if (out.Code == loop.OK) != e.allowed {
			t.Errorf("expand %s: %+v", e.id, out)
		}
	}
	out := bindings["recall"].Execute(t.Context(), provider.ToolCall{ID: "search", Name: "recall", Arguments: `{"query":"aurora"}`})
	if out.Code != loop.OK || !strings.Contains(string(out.Output), own.ID) || strings.Contains(string(out.Output), hidden.ID) || strings.Contains(string(out.Output), foreign.ID) {
		t.Fatalf("scoped search: %+v", out)
	}
	for _, args := range []string{`{"query":"aurora","agent":"foreign"}`, `{"query":"aurora","limit":101}`, `{"query":"aurora","kinds":["unknown"]}`, `{"query":"aurora","speaker_kind":"unknown"}`} {
		out := bindings["recall"].Execute(t.Context(), provider.ToolCall{ID: "invalid", Name: "recall", Arguments: args})
		if out.Code != loop.Invalid {
			t.Errorf("invalid args accepted: %+v", out)
		}
	}
	forgetQueuedSource(t, r, own.ID)
	if _, err := r.expandRecall(t.Context(), r.agent, "event:"+own.ID, 0, 50); !errors.Is(err, compaction.ErrExpand) {
		t.Fatalf("retracted expansion: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.topicPacket(ctx, own, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled collection: %v", err)
	}
	r.channelBinding = &channels.Binding{Scope: channels.Scope{Tools: true}}
	if out := bindings["recall"].Execute(t.Context(), provider.ToolCall{ID: "revoked", Name: "recall", Arguments: `{"query":"aurora"}`}); out.Code != loop.Denied {
		t.Fatalf("changed scope still executes: %+v", out)
	}
}
