// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
)

func TestRecordedBreachCountsSpeakingAgentOnly(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	journal := ledger.New(db, nil)
	for _, tc := range []struct{ agent, text string }{
		{"a", "Done. " + strings.Repeat("가", 301)},
		{"b", "Complete."},
	} {
		f := filter(t, config())
		if _, err := f.Push(tc.text); err != nil {
			t.Fatal(err)
		}
		rec := recording()
		rec.Source.ID, rec.Source.AgentID = "source-"+tc.agent, tc.agent
		rec.CardEventID, rec.ResultID = "card-"+tc.agent, "result-"+tc.agent
		if _, err := f.Record(t.Context(), journal, rec); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Record(t.Context(), journal, rec); err != nil {
			t.Fatal(err)
		}
	}
	for agent, want := range map[string]int{"a": 1, "b": 0, "other": 0} {
		var count int
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), `SELECT coalesce(sum(json_extract(body,'$.budget_breach')),0) FROM conv_event WHERE scaffold_kind='reply_check' AND agent_id=?`, agent).Scan(&count)
		})
		if err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("agent %s budget breaches=%d; want %d", agent, count, want)
		}
		counts, err := reply.Breaches(t.Context(), db, agent)
		if err != nil || counts.Budget != want || (agent != "other" && counts.Replies != 1) || (agent == "other" && counts.Replies != 0) {
			t.Errorf("agent %s counters=%+v error=%v", agent, counts, err)
		}
	}
}
