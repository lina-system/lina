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
	t.Parallel()
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
