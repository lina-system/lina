// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestWorkerLegacyOutcomeReachesModelContext(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "worker-owner", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create the file and check it."})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.workerNotice(t.Context(), input, "worker-finish:legacy:turn", "Codex turn done.\nRecorded legacy result"); err != nil {
		t.Fatal(err)
	}
	events, err := r.events(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || strings.Contains(event.Body.Text(), "Recorded legacy result")
	}
	if !found {
		t.Fatal("canon worker outcome excluded from the model's history")
	}
}

func TestWorkerCompletionIsDurableScopedAndDeduplicated(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "owner", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create a file and read it back"})
	if err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "session", TurnID: "turn", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	entry := &workerSession{origin: r, input: input, thread: thread, workdir: t.TempDir(), packet: "create result.txt", output: "created result.txt: outcome"}
	event := worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Done}}
	for range 2 {
		if err := entry.finish(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	// No runtime session is present. Only the immutable canon record can answer.
	in, err := workerSchema("worker_snapshot", json.RawMessage(`{"session":"session"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.workerOperation(t.Context(), "worker_snapshot", "read", input, in, lease.Lease{})
	var recorded workerCompletion
	if err != nil || got.State != tools.Applied || json.Unmarshal(got.Body, &recorded) != nil || recorded.Live || recorded.Output != entry.output || recorded.Status != worker.Done || recorded.Workdir != entry.workdir {
		t.Fatalf("recorded result: %s %v", got.Body, err)
	}
	items, err := r.historyItems(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("raw completion appeared as a second speaker: %+v %v", items, err)
	}
	events, err := r.events(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.ScaffoldKind != "worker_completion" {
			continue
		}
		count++
		m, err := event.Message()
		if err != nil {
			t.Fatal(err)
		}
		evidence, untrusted := m.Evidence()
		if !untrusted || !evidence.Untrusted || !strings.Contains(evidence.Text, "outcome") {
			t.Fatalf("worker claim promoted to trusted input: %+v", evidence)
		}
	}
	if count != 1 {
		t.Fatalf("duplicate completion records: %d", count)
	}
	var admitted int
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='worker-finish'").Scan(&admitted)
	}); err != nil || admitted != 1 {
		t.Fatalf("follow-up inbox count=%d err=%v", admitted, err)
	}
	foreign := newConversationView(r, "other", "main:other")
	got, err = foreign.workerOperation(t.Context(), "worker_snapshot", "read", input, in, lease.Lease{})
	if err != nil || got.State != tools.Failed {
		t.Fatalf("foreign outcome disclosed: %+v %v", got, err)
	}
	in, err = workerSchema("worker_input", json.RawMessage(`{"session":"session","turn":"turn","generation":1,"binding":1,"text":"repeat"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.workerOperation(t.Context(), "worker_input", "read", input, in, lease.Lease{})
	if err != nil || got.State != tools.Failed {
		t.Fatalf("record authorized live control: %+v %v", got, err)
	}
}

func TestWorkerReadSchemaCannotEnlargeScope(t *testing.T) {
	for _, raw := range []string{`{"session":"s","path":"../secret"}`, `{"session":"s","path":"/secret"}`, `{"session":"s","path":"result","workdir":"/"}`, `{"session":"s","path":"result","operation":"write"}`} {
		if _, err := workerSchema("worker_read_file", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	in, err := workerSchema("worker_read_file", json.RawMessage(`{"session":"s","path":"result.txt"}`))
	if err != nil || in.Write {
		t.Fatalf("read scope=%+v %v", in, err)
	}
}

func TestWorkerCompletionCannotSupplyUserCancellation(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryTool, ledger.Input{ID: "worker-claim", ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: "worker:claim", Body: "stop this worker"})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]loop.Tool{}
	if err := r.prepareWorkerTools("followup", input, bindings); err != nil {
		t.Fatal(err)
	}
	got := bindings["worker_interrupt"].Execute(t.Context(), provider.ToolCall{ID: "injected", Name: "worker_interrupt", Arguments: `{"session":"session","turn":"turn","generation":1,"binding":1,"quote":"stop this worker"}`})
	if got.Code != loop.Denied {
		t.Fatalf("worker claim authorized cancellation: %+v", got)
	}
}

// Only Send is used by channelDeliveries; any other adapter operation is a test
// failure through the nil embedded adapter rather than a real platform call.
type workerOutcomeChannel struct {
	channelAdapter
	sent []gateway.Outbound
}

func (a *workerOutcomeChannel) Send(_ context.Context, _ lease.Lease, message gateway.Outbound) (gateway.SendResult, error) {
	a.sent = append(a.sent, message)
	return gateway.SendResult{Outcome: gateway.Delivered, MessageID: "delivered"}, nil
}

func TestWorkerCompletionReachesOriginatingChannelOnce(t *testing.T) {
	for _, platform := range []string{"telegram", "discord"} {
		t.Run(platform, func(t *testing.T) {
			r := runRuntime(t)
			ctx, now := t.Context(), time.Now()
			store := r.channels.store
			bot := channels.Bot{Platform: platform, Account: "fixture"}
			if err := store.Connect(ctx, bot, r.person); err != nil {
				t.Fatal(err)
			}
			code, err := store.PairingCode(ctx, bot, r.person, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Pair(ctx, bot, code, "42", "100", now); err != nil {
				t.Fatal(err)
			}
			address := channels.Address{Bot: bot, Chat: "100"}
			binding := channels.Binding{Address: address, Agent: r.agent, Person: r.person, Conversation: r.conversation, Scope: channels.Scope{Reads: channels.DMOnly, Writes: channels.ReplyOnly, Tools: true, Memory: true}}
			if err := store.Bind(ctx, binding); err != nil {
				t.Fatal(err)
			}
			inbound := gateway.Inbound{Address: address, NativeID: "native", Sender: "42", Cursor: "1", Direct: true, Body: []byte(`{"Text":"make the file"}`)}
			var delivery gateway.Delivery
			if err := gateway.New(r.db, func(_ context.Context, d gateway.Delivery) error { delivery = d; return nil }).Admit(ctx, inbound); err != nil {
				t.Fatal(err)
			}
			surface := ledger.Surface{Kind: "channel", Platform: platform, ChatID: address.Chat}
			input, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "channel-input:" + strconv.FormatInt(delivery.InboxID, 10), ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "make the file"})
			if err != nil {
				t.Fatal(err)
			}
			entry := workerSession{origin: r, input: input, thread: worker.Thread{ID: "worker", AttemptID: "attempt"}, workdir: t.TempDir(), packet: "make the file", output: "file made"}
			if err := entry.finish(ctx, worker.Event{Thread: worker.Thread{ID: "worker", TurnID: "done"}, Finish: &worker.Finish{Status: worker.Done}}); err != nil {
				t.Fatal(err)
			}
			var completion string
			if err := r.db.Read(ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(ctx, "SELECT id FROM conv_event WHERE scaffold_kind='worker_completion'").Scan(&completion)
			}); err != nil {
				t.Fatal(err)
			}
			_, err = r.journal().Admit(ctx, guard.EntryAgent, ledger.Input{ID: "checked-reply", ParentID: completion, ConversationID: r.conversation, AgentID: r.agent, Surface: surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Checked file outcome"})
			if err != nil {
				t.Fatal(err)
			}
			adapter := &workerOutcomeChannel{}
			for range 2 {
				if err := r.channels.channelDeliveries(ctx, bot, adapter); err != nil {
					t.Fatal(err)
				}
			}
			if len(adapter.sent) != 1 {
				t.Fatalf("worker completion deliveries=%d, want one", len(adapter.sent))
			}
			got := adapter.sent[0]
			if got.Address != address || got.ReplyTo != "native" || got.LedgerRef != "checked-reply" || got.Text != "Checked file outcome" {
				t.Fatalf("lost source address or result: %+v", got)
			}
		})
	}
}

func TestWorkerCompletionRecoversOriginalChannelScope(t *testing.T) {
	for _, threaded := range []bool{false, true} {
		name := "shared-main"
		if threaded {
			name = "thread-scope"
		}
		t.Run(name, func(t *testing.T) {
			r := controlRuntime(t)
			ctx, now := t.Context(), time.Now()
			store := r.channels.store
			connect := func(platform string) channels.Binding {
				t.Helper()
				bot := channels.Bot{Platform: platform, Account: "fixture"}
				if err := store.Connect(ctx, bot, r.person); err != nil {
					t.Fatal(err)
				}
				code, err := store.PairingCode(ctx, bot, r.person, now)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Pair(ctx, bot, code, "42", "100", now); err != nil {
					t.Fatal(err)
				}
				binding := channels.Binding{Address: channels.Address{Bot: bot, Chat: "100"}, Person: r.person, Agent: r.agent, Conversation: r.conversation, Scope: channels.Scope{Reads: channels.DMOnly, Writes: channels.ReplyOnly, Tools: true, Memory: true}}
				if err := store.Bind(ctx, binding); err != nil {
					t.Fatal(err)
				}
				return binding
			}
			discord, telegram := connect("discord"), connect("telegram")
			source := telegram
			if threaded {
				source = discord
				source.Chat = "group"
				source.Scope.Reads = channels.Addressed
				if err := store.Bind(ctx, source); err != nil {
					t.Fatal(err)
				}
				source.Topic = "thread"
			}
			source.Scope.Memory, source.Scope.Tools = false, false
			if err := store.Bind(ctx, source); err != nil {
				t.Fatal(err)
			}
			var delivery gateway.Delivery
			inbound := gateway.Inbound{Address: source.Address, NativeID: "source", Sender: "42", Cursor: "1", Direct: !threaded, Addressed: true, Body: []byte(`{"Text":"work request"}`)}
			if err := gateway.New(r.db, func(_ context.Context, d gateway.Delivery) error { delivery = d; return nil }).Admit(ctx, inbound); err != nil {
				t.Fatal(err)
			}
			completion := ledger.Event{ParentID: "channel-input:" + strconv.FormatInt(delivery.InboxID, 10), ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "channel"}}
			view, err := r.workerCompletionView(ctx, completion)
			if err != nil {
				t.Fatal(err)
			}
			if view.channelAddress != source.Address || view.channelBinding == nil || *view.channelBinding != source {
				t.Fatalf("completion recovered another channel's scope: address=%+v binding=%+v, want %+v", view.channelAddress, view.channelBinding, source)
			}
			if err := view.validateChannel(ctx); err != nil {
				t.Fatal(err)
			}
			// A revoked original route cannot fall back to the same persona on another
			// platform, even though that other binding still has tools and memory.
			if err := store.Disconnect(ctx, source.Bot, r.person); err != nil {
				t.Fatal(err)
			}
			if _, err := r.workerCompletionView(ctx, completion); err == nil {
				t.Fatal("completion fell back to an unrelated channel after revocation")
			}
		})
	}
}
