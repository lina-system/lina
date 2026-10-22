// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestChannelControlConversationStream(t *testing.T) {
	for _, scenario := range []string{"private", "refused-shared", "recovered-admission"} {
		t.Run(scenario, func(t *testing.T) {
			r := controlRuntime(t)
			binding := channels.Binding{Person: r.person, Agent: r.agent, Conversation: r.conversation, Scope: channels.PrivateScope()}
			event := gateway.Inbound{Address: channels.Address{Bot: channels.Bot{Platform: "telegram", Account: "fixture"}, Chat: "100"}, Direct: scenario != "refused-shared", Body: []byte(`{"text":"/progress"}`)}
			receipt, err := inbox.New(r.db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "channel-fixture", NativeID: "control"}})
			if err != nil {
				t.Fatal(err)
			}
			id := "channel-input:" + strconv.FormatInt(receipt.ID, 10)
			replyID := "channel-control:" + strconv.FormatInt(receipt.ID, 10)
			if scenario == "recovered-admission" {
				now := time.Now()
				_, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "channel", Platform: "telegram", ChatID: "100"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: "control", Body: "/progress"})
				if err != nil {
					t.Fatal(err)
				}
			}
			topic := "conversation:" + r.conversation
			initial, sub, err := r.topics.Subscribe(topic, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			calls := 0
			op := r.identity.Operations[protocol.KindProgressDigestGet]
			handle := op.Handle
			op.Handle = func(ctx context.Context, session clientsrv.Session, envelope protocol.Envelope) (clientsrv.Result, error) {
				calls++
				start, observer, err := r.topics.Subscribe(topic, nil)
				if err != nil {
					t.Fatal(err)
				}
				observer.Close()
				found := false
				for _, item := range start.Snapshot.Items {
					found = found || item.Key == id
				}
				if !found {
					t.Error("committed command not published before control execution")
				}
				return handle(ctx, session, envelope)
			}
			r.identity.Operations[protocol.KindProgressDigestGet] = op
			for range 2 {
				handled, err := r.channels.channelControl(t.Context(), r, event, binding, channelInput{id: receipt.ID, native: "control"})
				if err != nil || !handled {
					t.Fatalf("control: handled=%t err=%v", handled, err)
				}
			}
			wantCalls := 1
			if scenario == "refused-shared" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("control executed %d times, want %d", calls, wantCalls)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			for i, key := range []string{id, replyID} {
				frame, err := sub.Next(ctx)
				if err != nil {
					t.Fatalf("committed control message %s missing from live subscription: %v", key, err)
				}
				var item protocol.ItemDone
				if frame.Topic != topic || frame.Event.Kind != protocol.KindItemDone || frame.Event.Key != key || json.Unmarshal(frame.Event.Data, &item) != nil {
					t.Fatalf("wrong live control message: %+v", frame)
				}
				stored, err := r.journal().Get(ctx, key)
				if err != nil || item.Text == nil || *item.Text != stored.Body.Text() || item.Speaker == nil || string(*item.Speaker) != string(stored.SpeakerKind) || item.AgentID == nil || *item.AgentID != r.agent {
					t.Fatalf("live control differs from committed message: %+v err=%v", item, err)
				}
				if i == 0 && (item.PersonID == nil || *item.PersonID != r.person) || i == 1 && (item.PersonID != nil || stored.ParentID != id) {
					t.Fatalf("control attribution changed: %+v", item)
				}
			}
			start, observer, err := r.topics.Subscribe(topic, nil)
			if err != nil {
				t.Fatal(err)
			}
			observer.Close()
			if start.Snapshot.Cursor.Seq != initial.Snapshot.Cursor.Seq+2 {
				t.Fatalf("duplicate input republished messages: seq=%d", start.Snapshot.Cursor.Seq)
			}
			history, err := r.historyItems(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, item := range history {
				keys = append(keys, item.key)
			}
			if !reflect.DeepEqual(keys, []string{id, replyID}) {
				t.Fatalf("history differs from live order: %v", keys)
			}
		})
	}
}
