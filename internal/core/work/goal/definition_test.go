// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestGoalDefinitionLivesInRecordsNotExecutionLedger(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.Summary = "synthetic unique outcome"
	g.Scope.Includes = "synthetic unique boundary"
	g.Decisions = []GoalDecision{{Statement: "synthetic unique decision", MessageID: "person-message"}}
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	saved := s.Goals[0]
	if saved.RecordID == "" || saved.RecordRevision == "" || saved.Summary != g.Summary {
		t.Fatalf("missing canonical goal record: %+v", saved)
	}
	r, rev, err := db.Definitions.Read(t.Context(), saved.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if rev != saved.RecordRevision || !strings.Contains(string(r.Data), g.Summary) {
		t.Fatal("record does not contain the goal definition")
	}
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, table := range []string{"work_events", "work_records"} {
			rows, err := tx.QueryContext(t.Context(), "SELECT body FROM "+table)
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw string
				if err = rows.Scan(&raw); err != nil {
					rows.Close()
					return err
				}
				for _, private := range []string{g.Summary, g.Scope.Includes, g.Decisions[0].Statement} {
					if strings.Contains(raw, private) {
						t.Errorf("goal prose duplicated in %s", table)
					}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestRecordEditFencesGoalUntilManagerInterpretsRevision(t *testing.T) {
	_, db := testJournal(t)
	e, _ := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	g := oneGoal(readyTask("a"))
	if err := e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Snapshot(t.Context())
	saved := before.Goals[0]
	r, rev, err := db.Definitions.Read(t.Context(), saved.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nChanged outcome from the person.\n")...)}, g.Person, g.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Snapshot(t.Context())
	updated := after.Goals[0]
	if updated.RecordRevision != edited || updated.Revision != saved.Revision+1 || !updated.DefinitionPending {
		t.Fatalf("record edit ignored: %+v", updated)
	}
	if len(after.Actions) != 1 || after.Actions[0].Kind != PlanGoal {
		t.Fatalf("stale goal dispatched: %+v", after.Actions)
	}
}
