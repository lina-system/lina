// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestGrowthMigrationPreservesPersonaAndEnforcesRevisionReferences(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	index := len(all)
	all = append(all, Migration{Version: index + 1, SQL: GrowthSchema})
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO agents VALUES('fixture-agent','fixture-name','',1,'{}',0);
 INSERT INTO agent_revisions VALUES('fixture-agent',1,'fixture-commit','fixture-person');
 INSERT INTO agent_preparations VALUES('fixture-turn','fixture-agent',1);`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations, cfg.Epoch = all, cfg.Epoch+1
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var commit string
		err := tx.QueryRowContext(t.Context(), `SELECT r.commit_id FROM agent_preparations p JOIN agent_revisions r USING(agent_id,revision) WHERE turn_id='fixture-turn'`).Scan(&commit)
		if err == nil && commit != "fixture-commit" {
			t.Fatalf("changed prepared persona: %q", commit)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO agent_growth_steps VALUES('fixture-agent','fixture-step',2,1,'fixture-hash','fixture-notice',NULL)`)
		return err
	}); err == nil {
		t.Fatal("notice committed without its revision")
	}
}
