// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"os"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/turn"
)

func TestOutcomeMigrationSupportsFreshAndPreBudgetDatabases(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[upgrade], func(t *testing.T) {
			var db *canon.Store
			if upgrade {
				previous, err := os.ReadFile("testdata/pre_budget.sql")
				if err != nil {
					t.Fatal(err)
				}
				old, cfg := database(t, string(previous), turn.ControlSchema)
				id := admit(t, old)
				if err := old.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), "INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES('old','conversation',?,'completed')", id)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := old.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				cfg.Epoch++
				cfg.Migrations[len(cfg.Migrations)-2].SQL = turn.Schema
				cfg.Migrations = append(cfg.Migrations, canon.Migration{Version: len(cfg.Migrations) + 1, SQL: turn.OutcomeSchema})
				db = open(t, cfg)
				prior, err := turn.New(db).Read(t.Context(), "old")
				if err != nil || prior.State != turn.Completed {
					t.Fatalf("old turn changed: %+v %v", prior, err)
				}
			} else {
				db, _ = database(t, turn.Schema, turn.ControlSchema, turn.OutcomeSchema)
			}
			id, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: "new"}, Body: []byte("new input")})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.New(db).Claim(t.Context(), "new", "conversation", id.ID); err != nil {
				t.Fatal(err)
			}
			outcome, err := turn.New(db).Outcome(t.Context(), "new")
			if err != nil || outcome.Gathered.Input != "new input" {
				t.Fatalf("new outcome: %+v %v", outcome, err)
			}
		})
	}
}
