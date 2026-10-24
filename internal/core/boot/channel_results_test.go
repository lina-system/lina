// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/kit/result"
)

func TestTypedResultsReachOriginatingChannelOnce(t *testing.T) {
	for _, platform := range []string{"telegram", "discord"} {
		t.Run(platform, func(t *testing.T) {
			r := runRuntime(t)
			ctx, now := t.Context(), time.Now()
			bot := channels.Bot{Platform: platform, Account: "fixture"}
			store := r.channels.store
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
			if err := store.Bind(ctx, channels.Binding{Address: address, Agent: r.agent, Person: r.person, Conversation: r.conversation, Scope: channels.PrivateScope()}); err != nil {
				t.Fatal(err)
			}
			inbound := gateway.Inbound{Address: address, NativeID: "native", Sender: "42", Cursor: "1", Direct: true, Body: []byte(`{"text":"compare options"}`)}
			var delivery gateway.Delivery
			if err := gateway.New(r.db, func(_ context.Context, d gateway.Delivery) error { delivery = d; return nil }).Admit(ctx, inbound); err != nil {
				t.Fatal(err)
			}
			surface := ledger.Surface{Kind: "channel", Platform: platform, ChatID: address.Chat}
			input, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "channel-input:" + strconv.FormatInt(delivery.InboxID, 10), ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "compare options"})
			if err != nil {
				t.Fatal(err)
			}
			var sources []string
			for _, comparison := range []string{"verdict-then-card", "verdict-then-table"} {
				shapes := reply.Shapes{Status: "verdict-first", Comparison: comparison, Candidates: 1, ThirdParty: "recipient-only"}
				service, err := results.New(r.db, results.Config{Journal: measuredResultJournal{LedgerJournal: results.LedgerJournal{Surface: surface, ParentID: input.ID}, config: conversationReplyConfig(&shapes)}})
				if err != nil {
					t.Fatal(err)
				}
				card, err := service.Present(ctx, result.Scope{PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, TurnID: "turn:" + input.ID}, result.PresentRequest{Type: result.TableType, Title: "Options", Summary: "Choose A.", Body: json.RawMessage(`{"columns":[{"name":"Choice","type":"text","unit":""}],"rows":[["A"],["B"]]}`)})
				if err != nil {
					t.Fatal(err)
				}
				var source string
				if err := r.db.Read(ctx, func(tx *canon.Tx) error {
					return tx.QueryRowContext(ctx, `SELECT event_id FROM result_heads WHERE id=?`, card.ID).Scan(&source)
				}); err != nil {
					t.Fatal(err)
				}
				sources = append(sources, source)
			}
			adapter := &workerOutcomeChannel{}
			for range 2 {
				if err := r.channels.channelDeliveries(ctx, bot, adapter); err != nil {
					t.Fatal(err)
				}
			}
			if len(adapter.sent) != 2 {
				t.Fatalf("typed result deliveries=%d; want 2", len(adapter.sent))
			}
			for i, message := range adapter.sent {
				if message.Address != address || message.ReplyTo != "native" || message.LedgerRef != sources[i] || !strings.Contains(message.Text, "Choose A.") {
					t.Errorf("wrong destination or result: %+v", message)
				}
			}
			if !strings.Contains(adapter.sent[0].Text, "Options") || !strings.Contains(adapter.sent[0].Text, "lina://conversation/") {
				t.Fatal("card lost its caption or conversation link")
			}
			if !strings.Contains(adapter.sent[1].Text, "Choice") || !strings.Contains(adapter.sent[1].Text, "B") || strings.Contains(adapter.sent[1].Text, "lina://") {
				t.Fatal("inline table was replaced by a card")
			}
		})
	}
}

func TestBindingChipStaysOnDirectChannelReply(t *testing.T) {
	for _, platform := range []string{"telegram", "discord"} {
		t.Run(platform, func(t *testing.T) {
			r := runRuntime(t)
			ctx, now := t.Context(), time.Now()
			fixtureRecord(t, r, "plants", "todo", "open", "", "Plants", "Water them.")
			bot := channels.Bot{Platform: platform, Account: "fixture"}
			store := r.channels.store
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
			if err := store.Bind(ctx, channels.Binding{Address: address, Agent: r.agent, Person: r.person, Conversation: r.conversation, Scope: channels.PrivateScope()}); err != nil {
				t.Fatal(err)
			}
			inbound := gateway.Inbound{Address: address, NativeID: "native", Sender: "42", Cursor: "1", Direct: true, Body: []byte(`{"text":"Plants"}`)}
			var delivery gateway.Delivery
			if err := gateway.New(r.db, func(_ context.Context, d gateway.Delivery) error { delivery = d; return nil }).Admit(ctx, inbound); err != nil {
				t.Fatal(err)
			}
			inputID := "channel-input:" + strconv.FormatInt(delivery.InboxID, 10)
			appendEvent := func(entry guard.Entry, id, parent, scaffold, body string) {
				t.Helper()
				person := ""
				if entry == guard.EntryOwner {
					person = r.person
				}
				_, err := r.journal().Admit(ctx, entry, ledger.Input{ID: id, ParentID: parent, ConversationID: r.conversation, AgentID: r.agent, PersonID: person, Surface: ledger.Surface{Kind: "channel", Platform: platform, ChatID: address.Chat}, ScaffoldKind: scaffold, Locator: "fixture:" + id, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: body})
				if err != nil {
					t.Fatal(err)
				}
			}
			appendEvent(guard.EntryOwner, inputID, "", "", "Plants")
			appendEvent(guard.EntryAgent, "reply", inputID, "", "Working on the plants.")
			appendEvent(guard.EntryTool, "completion", inputID, "worker_completion", "Done.")
			appendEvent(guard.EntryAgent, "follow-up", "completion", "", "The plants are watered.")
			adapter := &workerOutcomeChannel{}
			for range 2 {
				if err := r.channels.channelDeliveries(ctx, bot, adapter); err != nil {
					t.Fatal(err)
				}
			}
			chips := 0
			for _, message := range adapter.sent {
				if strings.Contains(message.Text, "/move") {
					chips++
					if message.LedgerRef != "reply" {
						t.Errorf("chip attached to %q; want direct reply", message.LedgerRef)
					}
				}
			}
			if chips != 1 {
				t.Fatalf("sent %d chips; want one across repeated delivery scans", chips)
			}
		})
	}
}
