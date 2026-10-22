// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestForwardedHistoryRespectsCurrentInputBoundary(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "forwarded-history"
	for _, row := range []struct {
		id    string
		entry guard.Entry
	}{
		{"earlier", guard.EntryForwarded},
		{"owner", guard.EntryOwner},
		{"current", guard.EntryForwarded},
		{"queued-forward", guard.EntryForwarded},
		{"queued-owner", guard.EntryOwner},
		{"reply", guard.EntryAgent},
	} {
		person := ""
		if row.entry == guard.EntryOwner {
			person = r.person
		}
		_, err := r.journal().Admit(t.Context(), row.entry, ledger.Input{
			ID: row.id, ConversationID: r.conversation, AgentID: agent, PersonID: person,
			Surface:    ledger.Surface{Kind: "channel", Platform: "telegram", ChatID: "100"},
			OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed", Locator: row.id, Body: row.id,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := r.events(t.Context(), "current")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if !reflect.DeepEqual(ids, []string{"earlier", "owner", "current", "reply"}) {
		t.Fatalf("model history crossed input boundary or lost forwarded events: %v", ids)
	}
	history, err := r.historyItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, item := range history {
		ids = append(ids, item.eventID)
	}
	if !reflect.DeepEqual(ids, []string{"earlier", "owner", "current", "queued-forward", "queued-owner", "reply"}) {
		t.Fatalf("restored transcript lost forwarded events: %v", ids)
	}
	r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.topics.Close)
	if err := r.seedHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	start, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var forwarded int
	for _, item := range start.Snapshot.Items {
		if item.Key != "earlier" && item.Key != "current" && item.Key != "queued-forward" {
			continue
		}
		var payload protocol.ItemDone
		if err := json.Unmarshal(item.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Speaker == nil || *payload.Speaker != "forwarded" || payload.PersonID != nil || payload.EchoKey != nil {
			t.Fatalf("restored forwarded input became owner speech: %+v", payload)
		}
		forwarded++
	}
	if forwarded != 3 {
		t.Fatalf("restored forwarded items=%d", forwarded)
	}
}
