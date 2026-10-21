// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationHistoryLegacyAndUncommittedPositions(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "history"
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
	if err := r.publish(t.Context(), protocol.KindOutputDelta, "uncommitted", "partial"); err != nil {
		t.Fatal(err)
	}
	if err := r.publish(t.Context(), protocol.KindOutputDelta, "new-reply", "new"); err != nil {
		t.Fatal(err)
	}
	admit("new-input", "", "", guard.EntryOwner)
	text := "new-input"
	if err := r.publish(t.Context(), protocol.KindItemDone, "new-input", protocol.ItemDone{Text: &text}); err != nil {
		t.Fatal(err)
	}
	admit("new-reply", "old-input", "", guard.EntryAgent)
	// Simulate a committed outcome whose publication was lost to shutdown.
	admit("unpublished-reply", "new-input", "", guard.EntryAgent)
	want := []string{"old-input", "old-reply", "new-reply", "new-input", "unpublished-reply"}
	assertKeys(want)
	assertKeys(want) // Repeated recovery cannot move or duplicate any item.
}

func TestSeedHistoryPreservesPartialProjectionAndPosition(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "history"
	ctx := t.Context() // runtime.ctx deliberately remains nil.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := ledger.New(r.db, nil).AdmitBatch(ctx, []ledger.Admission{
		{Entry: guard.EntryOwner, Input: ledger.Input{ID: "input", ConversationID: r.conversation, AgentID: agent, PersonID: r.person, Locator: "fixture-input", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", Body: "input"}},
		{Entry: guard.EntrySystem, Input: ledger.Input{ID: "notice", ParentID: "input", ConversationID: r.conversation, AgentID: agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", ScaffoldKind: "interruption", Body: "stopped"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	long := "Public text. " + strings.Repeat("unfinished text ", 100)
	err = r.db.Write(ctx, func(tx *canon.Tx) error {
		receipt, err := inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "fixture", NativeID: "input"}})
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES('turn',?,?,'aborted')`, r.conversation, receipt.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO turn_control(turn_id,input_event,notice_event) VALUES('turn','input','notice')`); err != nil {
			return err
		}
		// The first visible item has a larger output index. Legacy bootstrap
		// must retain checkpoint insertion order, not sort by provider index.
		_, err = tx.ExecContext(ctx, `INSERT INTO turn_partials(turn_id,round,ordinal,body) VALUES('turn',0,9,?),('turn',0,3,'short partial')`, long)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.topics.Close)
	if err := r.seedHistory(ctx); err != nil {
		t.Fatal(err)
	}
	start, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var keys []string
	for _, item := range start.Snapshot.Items {
		keys = append(keys, item.Key)
	}
	if !reflect.DeepEqual(keys, []string{"input", "turn:1:9", "turn:1:3", "notice", "turn"}) {
		t.Fatalf("seed lost checkpoint order: %v", keys)
	}
	card := start.Snapshot.Items[1]
	var result protocol.Result
	if err := json.Unmarshal(card.Data, &result); err != nil {
		t.Fatal(err)
	}
	if card.Kind != protocol.KindResult || result.Text == nil || *result.Text != long || result.ResultID != "result:turn:1:9" || result.Provenance.TurnID != "turn" {
		t.Fatalf("seed changed partial card identity/content: %+v", result)
	}
	var stored int
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM conversation_order WHERE conversation_id=?`, r.conversation).Scan(&stored)
	})
	if err != nil || stored != 4 {
		t.Fatalf("seed skipped durable positioning: count=%d err=%v", stored, err)
	}
}
