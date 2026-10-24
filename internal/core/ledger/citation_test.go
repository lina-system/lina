// SPDX-License-Identifier: Apache-2.0
package ledger_test

import (
	"database/sql"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestCitationScopeAndUTF8Boundaries(t *testing.T) {
	db, _ := database(t)
	store := ledger.New(db, nil)
	in := input("citation")
	in.Body = "한국어 source"
	admit(t, store, guard.EntryOwner, in)
	for _, tc := range []struct {
		person, agent string
		start, end    int64
		status, quote string
	}{
		{"owner", "agent", 0, 9, "", "한국어"},
		{"other", "agent", 0, 9, "restricted", ""},
		{"owner", "other", 0, 9, "restricted", ""},
		{"owner", "agent", 1, 9, "bytes_unavailable", ""},
		{"owner", "agent", 1, 1, "bytes_unavailable", ""},
		{"owner", "agent", 0, 999, "bytes_unavailable", ""},
	} {
		got, err := store.Citation(t.Context(), tc.person, tc.agent, canon.Citation{TargetKind: "event", TargetID: in.ID, SpanStart: sql.NullInt64{Int64: tc.start, Valid: true}, SpanEnd: sql.NullInt64{Int64: tc.end, Valid: true}})
		if err != nil || got.Status != tc.status || got.Quote != tc.quote {
			t.Errorf("%+v: %+v %v", tc, got, err)
		}
	}
}

// A messenger record is the person's record. Every agent may cite it even
// though its holder is the source rather than an agent.
func TestCitationOfMessengerRecordForAnyAgent(t *testing.T) {
	db, _ := database(t)
	store := ledger.New(db, nil)
	in := messengerInput("messenger-citation")
	in.Body = "I prefer jasmine tea."
	admit(t, store, guard.EntryMessengerPerson, in)
	got, err := store.Citation(t.Context(), "person", "agent-b", canon.Citation{TargetKind: "event", TargetID: in.ID,
		SpanStart: sql.NullInt64{Int64: 2, Valid: true}, SpanEnd: sql.NullInt64{Int64: 8, Valid: true}})
	if err != nil || got.Status != "" || got.Quote != "prefer" {
		t.Fatalf("messenger citation: %+v %v", got, err)
	}
	if got, err := store.Citation(t.Context(), "other", "agent-b", canon.Citation{TargetKind: "event", TargetID: in.ID}); err != nil || got.Status != "restricted" {
		t.Fatalf("foreign person: %+v %v", got, err)
	}
	// A message LINA sent belongs to the agent that sent it.
	own := messengerInput("messenger-own")
	own.PersonID = ""
	own.AgentID = "agent-a"
	own.Body = "On my way."
	admit(t, store, guard.EntryMessengerAgent, own)
	if got, err := store.Citation(t.Context(), "person", "agent-a", canon.Citation{TargetKind: "event", TargetID: own.ID}); err != nil || got.Status != "" {
		t.Fatalf("own write for its agent: %+v %v", got, err)
	}
}
