// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationHistoryLegacyAndUncommittedPositions(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.ctx, r.agent, r.conversation = t.Context(), agent, "history"
	var err error
	r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.topics.Close)
	admit := func(id, parent, scaffold string, entry guard.Entry) {
		t.Helper()
		person := ""
		if entry == guard.EntryOwner {
			person = r.person
		}
		// Equal timestamps deliberately cannot encode first appearance order.
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		_, err := ledger.New(r.db, nil).Admit(t.Context(), entry, ledger.Input{
			ID: id, ParentID: parent, ScaffoldKind: scaffold,
			ConversationID: r.conversation, AgentID: agent, PersonID: person,
			Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now,
			TimeQuality: "recorded", Body: id,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	admit("old-input", "", "", guard.EntryOwner)
	admit("old-reply", "old-input", "", guard.EntryAgent)
	admit("old-card", "old-reply", "result", guard.EntryAgent)
	assertKeys := func(want []string) {
		t.Helper()
		items, err := r.historyItems(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, item := range items {
			keys = append(keys, item.key)
		}
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("history keys=%v, want %v", keys, want)
		}
	}
	assertKeys([]string{"old-input", "old-reply"})
	// An unfinished streamed key has a position but no committed content.
	if err := r.publish(protocol.KindOutputDelta, "uncommitted", "partial"); err != nil {
		t.Fatal(err)
	}
	if err := r.publish(protocol.KindOutputDelta, "new-reply", "new"); err != nil {
		t.Fatal(err)
	}
	admit("new-input", "", "", guard.EntryOwner)
	text := "new-input"
	if err := r.publish(protocol.KindItemDone, "new-input", protocol.ItemDone{Text: &text}); err != nil {
		t.Fatal(err)
	}
	admit("new-reply", "old-input", "", guard.EntryAgent)
	// Simulate a committed outcome whose publication was lost to shutdown.
	admit("unpublished-reply", "new-input", "", guard.EntryAgent)
	want := []string{"old-input", "old-reply", "new-reply", "new-input", "unpublished-reply"}
	assertKeys(want)
	assertKeys(want) // Repeated recovery cannot move or duplicate any item.
}
