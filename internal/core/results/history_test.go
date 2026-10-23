// SPDX-License-Identifier: Apache-2.0
package results

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/result"
)

func TestResultStorageEventsNeverExposeBodyToAssembledHistory(t *testing.T) {
	t.Parallel()
	s, db, _, _ := setup(t)
	first := present(t, s)
	p := proposal()
	p.ID = first.ID
	p.BaseRevision = first.Revision
	p.Title = "Updated"
	current, err := s.Present(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(t.Context(), scope, result.SaveRequest{ID: current.ID, Revision: current.Revision, Destination: result.Destination{Kind: "resource"}}); err != nil {
		t.Fatal(err)
	}
	l := ledger.New(db, nil)
	card, _ := json.Marshal(current)
	acknowledgement, _ := json.Marshal(struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}{"function_call_output", "present", string(card)})
	now := time.Now()
	if _, err := l.Admit(t.Context(), guard.EntryTool, ledger.Input{ID: "acknowledgement", ConversationID: scope.ConversationID, AgentID: scope.AgentID, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "provider_item", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: "present_result", Body: string(acknowledgement)}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT id FROM conv_event ORDER BY rowid")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	var events []ledger.Event
	for _, id := range ids {
		e, err := l.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if len(events) != 4 {
		t.Fatalf("expected creation, revision, save and tool acknowledgement; got %d", len(events))
	}
	items, err := assemble.History(events, scope.ConversationID, scope.AgentID)
	if err != nil || len(items) != 1 || !bytes.Equal(items[0], acknowledgement) {
		t.Fatalf("result storage body reached model history: items=%s error=%v", items, err)
	}
}
