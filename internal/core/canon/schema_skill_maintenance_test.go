// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"errors"
	"testing"
)

func TestSkillMaintenanceMigrationPreservesExistingSkills(t *testing.T) {
	t.Parallel()
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
	if all[index].Version != 36 || all[index-1].Version != 35 {
		t.Fatalf("skill maintenance version=%d, predecessor=%d; want 36 after published 35", all[index].Version, all[index-1].Version)
	}
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO skill_versions VALUES('fixture',1,'commit','fixture','description','hash',0); INSERT INTO skill_heads VALUES('fixture',1); INSERT INTO skill_undone VALUES('fixture','rejected');
INSERT INTO work_waiting_rounds VALUES('round','goal','person',123,X'01');
INSERT INTO work_waiting_decisions(round_id,kind,message_id,record) VALUES('round','answer','message',X'02');`)
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
		var deadline int
		var kind, message, roundRecord, decisionRecord string
		if err := tx.QueryRowContext(t.Context(), `SELECT r.deadline,d.kind,d.message_id,hex(r.record),hex(d.record) FROM work_waiting_rounds r JOIN work_waiting_decisions d ON d.round_id=r.id WHERE r.id='round'`).Scan(&deadline, &kind, &message, &roundRecord, &decisionRecord); err != nil {
			return err
		}
		if deadline != 123 || kind != "answer" || message != "message" || roundRecord != "01" || decisionRecord != "02" {
			t.Fatal("upgrade changed published waiting history")
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

func TestSkillMaintenancePublishedCatalogRejectsMismatchedTables(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"skill_maintenance", "skill_maintenance_members", "skill_maintenance_notices"} {
		for _, missing := range []bool{false, true} {
			mode := "premature"
			if missing {
				mode = "missing"
			}
			t.Run(table+"/"+mode, func(t *testing.T) {
				cfg := storeConfig(t)
				cfg.Migrations = Migrations()
				if !missing {
					cfg.Migrations = cfg.Migrations[:23]
				}
				db := openStore(t, cfg)
				if err := db.Write(t.Context(), func(tx *Tx) error {
					query := "CREATE TABLE " + table + " (id TEXT)"
					if missing {
						query = "DROP TABLE " + table
					}
					_, err := tx.ExecContext(t.Context(), query)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := validatePublishedCatalog(t.Context(), cfg.Path, len(cfg.Migrations), Migrations()); !errors.Is(err, ErrDevelopmentSchema) {
					t.Fatalf("%s table was not rejected as an incompatible catalog: %v", mode, err)
				}
			})
		}
	}
}
