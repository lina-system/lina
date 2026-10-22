// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestSkillMaintenanceMigrationPreservesExistingSkills(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == skillMaintenanceSchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("missing migration")
	}
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO skill_versions VALUES('fixture',1,'commit','fixture','description','hash',0); INSERT INTO skill_heads VALUES('fixture',1); INSERT INTO skill_undone VALUES('fixture','rejected');`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = all
	cfg.Epoch++
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var commit, hash string
		if err := tx.QueryRowContext(t.Context(), `SELECT v.commit_id,u.content_hash FROM skill_heads h JOIN skill_versions v ON v.id=h.id AND v.revision=h.revision JOIN skill_undone u ON u.id=h.id WHERE h.id='fixture'`).Scan(&commit, &hash); err != nil {
			return err
		}
		if commit != "commit" || hash != "rejected" {
			t.Fatal("upgrade changed existing skill history")
		}
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM skill_maintenance_notices").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("migration invented notices")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
