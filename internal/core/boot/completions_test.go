// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
	"time"
)

func TestCompletionStartupRequiresInitializedRuntime(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	defer r.lanes.Close(context.Background())
	r.work = work.New(r.db)
	r.route = &modelpath.ResponsesConfig{}
	err := r.startCompletions(t.Context())
	if r.completionStop != nil {
		r.completionStop()
		<-r.completionDone
	}
	if err == nil {
		t.Fatal("completion delivery started before background service and final lanes were initialized")
	}
}

func TestCompletionInputIncludedInModelContext(t *testing.T) {
	r := controlRuntime(t)
	first, err := r.agents.FirstRun(t.Context(), r.person, true)
	if err != nil {
		t.Fatal(err)
	}
	view := newConversationView(r, first.Agent.ID, "main:"+first.Agent.ID)
	now := time.Now()
	event, err := view.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: "completion-input", ConversationID: view.conversation, AgentID: view.agent, Surface: ledger.Surface{Kind: "manager"}, ScaffoldKind: "work_completion", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: `{"receipt":"saved"}`})
	if err != nil {
		t.Fatal(err)
	}
	items, _, _, events, err := view.modelContext(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("completion omitted from manager context: items=%d events=%v", len(items), events)
	}
}

func TestCompletionPreservesChannelScope(t *testing.T) {
	for _, kind := range []string{"unchanged", "tools-revoked", "memory-widened", "thread-rebound", "private"} {
		t.Run(kind, func(t *testing.T) {
			r := controlRuntime(t)
			ctx := t.Context()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			first, err := r.agents.FirstRun(ctx, r.person, true)
			must(err)
			store := channels.New(r.db)
			bot := channels.Bot{Platform: "discord", Account: "fixture"}
			now := time.Now()
			must(store.Connect(ctx, bot, r.person))
			code, err := store.PairingCode(ctx, bot, r.person, now)
			must(err)
			must(store.Pair(ctx, bot, code, "42", "100", now))
			binding := channels.Binding{Address: channels.Address{Bot: bot, Chat: "group"}, Person: r.person, Agent: first.Agent.ID, Conversation: "group-conversation", Scope: channels.Scope{Reads: channels.Addressed, Writes: channels.ReplyOnly, Tools: true, Memory: false}}
			if kind == "private" {
				binding.Chat = "100"
				binding.Conversation = "main:" + first.Agent.ID
				binding.Scope.Reads = channels.DMOnly
			}
			must(store.Bind(ctx, binding))
			address := binding.Address
			if kind == "thread-rebound" {
				address.Topic = "thread"
			}
			runner := newConversationView(r, first.Agent.ID, binding.Conversation)
			runner.channelBinding = &binding
			runner.channelAddress = address
			bindings := map[string]loop.Tool{}
			must(runner.prepareWorkTools("turn:fixture", bindings))
			result := bindings["prepare_work"].Execute(ctx, provider.ToolCall{ID: "create", Name: "prepare_work", Arguments: `{"task_id":"channel-task","title":"Create a report","done":"Report exists","workspace":{}}`})
			if result.Code != loop.OK {
				t.Fatal(result)
			}
			card, err := r.work.Get(ctx, "channel-task")
			must(err)
			if kind == "tools-revoked" {
				changed := binding
				changed.Scope.Tools = false
				must(store.Bind(ctx, changed))
			}
			if kind == "memory-widened" {
				changed := binding
				changed.Scope.Memory = true
				must(store.Bind(ctx, changed))
			}
			if kind == "thread-rebound" {
				changed := binding
				changed.Address = address
				changed.Scope.Tools = false
				must(store.Bind(ctx, changed))
			}
			reaction, err := r.completionRunner(ctx, card)
			if kind == "unchanged" || kind == "private" {
				must(err)
				if reaction.channelBinding == nil || reaction.channelBinding.Scope.Memory || !reaction.channelBinding.Scope.Tools || reaction.channelAddress != address {
					t.Fatal("completion lost its originating channel scope")
				}
				must(reaction.validateChannel(ctx))
				_, err = r.Memory(ctx, clientsrv.Session{PersonID: r.person}, protocol.MemoryRequest{Operation: str("remember"), AgentID: &first.Agent.ID, Holder: str("agent"), Text: str("Private completion fixture")})
				must(err)
				shared, err := r.router.channelView(ctx, binding)
				must(err)
				_, visible, _, _, err := shared.modelContext(ctx, "")
				must(err)
				if len(visible) == 0 {
					t.Fatal("privacy fixture has no personal memory")
				}
				_, restricted, _, _, err := reaction.modelContext(ctx, "")
				must(err)
				if len(restricted) != 0 {
					t.Fatal("completion exposed disabled personal memory")
				}

			} else if !errors.Is(err, channels.ErrDenied) {
				t.Fatalf("changed channel authority accepted: %v", err)
			}
		})
	}
}
