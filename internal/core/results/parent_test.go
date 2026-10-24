// SPDX-License-Identifier: Apache-2.0
package results

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/result"
)

func TestResultParentMustBelongToTheTurnScope(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	now := time.Now()
	surface := ledger.Surface{Kind: "tui"}
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "input", ConversationID: scope.ConversationID, AgentID: scope.AgentID, PersonID: scope.PersonID, Surface: surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Compare the options."})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ parent, agent, conversation string }{
		{"missing", scope.AgentID, scope.ConversationID},
		{"input", "other-agent", scope.ConversationID},
		{"input", scope.AgentID, "other-conversation"},
	} {
		s, err := New(db, Config{Journal: LedgerJournal{Surface: surface, ParentID: tc.parent}})
		if err != nil {
			t.Fatal(err)
		}
		target := scope
		target.AgentID, target.ConversationID = tc.agent, tc.conversation
		if _, err := s.Present(t.Context(), target, proposal()); !errors.Is(err, ErrOutOfScope) {
			t.Fatalf("foreign parent accepted: %+v %v", tc, err)
		}
		if count(t, db, "result_heads") != 0 || count(t, db, "conv_event") != 1 {
			t.Fatal("refusal committed a partial result")
		}
	}
	s, err := New(db, Config{Journal: LedgerJournal{Surface: surface, ParentID: "input"}})
	if err != nil {
		t.Fatal(err)
	}
	card := present(t, s)
	var parent string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT e.parent_id FROM result_heads h JOIN conv_event e ON e.id=h.event_id WHERE h.id=?`, card.ID).Scan(&parent)
	}); err != nil || parent != "input" {
		t.Fatalf("parent=%q error=%v", parent, err)
	}
	if _, err := s.Read(t.Context(), scope, result.ReadRequest{ID: card.ID}); err != nil {
		t.Fatal(err)
	}
}
