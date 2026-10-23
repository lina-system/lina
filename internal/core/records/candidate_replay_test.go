// SPDX-License-Identifier: Apache-2.0
package records

import (
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestCandidateAliasesCannotRepeatFinishedAdoption(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"completed", "canceled"} {
		for _, alias := range []string{"replay", "view", "chain"} {
			t.Run(state+"/"+alias, func(t *testing.T) {
				s, root, db := setup(t)
				in := proposal("original", "Send notes")
				seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
				added, err := s.ProposeCandidate(t.Context(), actor, in)
				if err != nil || added.Status != "added" {
					t.Fatal(added, err)
				}
				r, rev, err := s.Read(t.Context(), added.RecordID)
				if err != nil {
					t.Fatal(err)
				}
				data := strings.Replace(string(r.Data), `state: "open"`, `state: "`+state+`"`, 1)
				finished := commit(t, s, Change{ID: r.ID, Path: r.Path, Base: rev, Data: []byte(data)})
				now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				admitAlias := func(id, replay, view string) {
					t.Helper()
					e := ledger.Input{ID: id, PersonID: actor.Person, AgentID: actor.Agent, ConversationID: "main:agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: in.Quotation, ReplayOf: replay}
					if view != "" {
						e.Relations = []ledger.Relation{{Kind: "view_of", TargetID: view}}
					}
					if _, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, e); err != nil {
						t.Fatal(err)
					}
				}
				switch alias {
				case "replay":
					admitAlias(alias, in.Source, "")
				case "view":
					admitAlias(alias, "", in.Source)
				case "chain":
					admitAlias("replay", in.Source, "")
					admitAlias(alias, "", "replay")
				}
				recovered, err := Open(root, db)
				if err != nil {
					t.Fatal(err)
				}
				defer recovered.Close()
				in.Source = alias
				in.Title = " send   NOTES "
				got, err := recovered.ProposeCandidate(t.Context(), actor, in)
				if err != nil || got.Status != "pending" || got.RecordID != "" || got.Undo != "" || got.Notice != "" {
					t.Fatalf("alias repeated %s adoption: %+v, %v", state, got, err)
				}
				retry, err := recovered.ProposeCandidate(t.Context(), actor, in)
				if err != nil || retry != got {
					t.Fatal("pending retry changed receipt", retry, err)
				}
				rows, err := recovered.Query(t.Context(), Params{Person: actor.Person, Kind: "todo"})
				if err != nil || len(rows) != 1 || rows[0].ID != added.RecordID || rows[0].State != state {
					t.Fatal("replay changed records", rows, err)
				}
				_, after, err := recovered.Read(t.Context(), added.RecordID)
				if err != nil || after != finished {
					t.Fatal("replay changed saved revision", after, err)
				}
				var operations int
				if err = db.Read(t.Context(), func(tx *canon.Tx) error {
					return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM record_operations").Scan(&operations)
				}); err != nil || operations != 1 {
					t.Fatal("replay published an operation", operations, err)
				}
				// A new commitment with the same action is not a historical replay.
				fresh := proposal("new-commitment", "Send notes")
				seedCandidateSource(t, db, fresh.Source, fresh.Quotation, actor.Person, guard.EntryOwner)
				if c, err := recovered.ProposeCandidate(t.Context(), actor, fresh); err != nil || c.Status != "added" || c.RecordID == added.RecordID {
					t.Fatal("independent commitment suppressed", c, err)
				}
			})
		}
	}
}
