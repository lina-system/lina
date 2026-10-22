// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestBackupFromSkillMaintenanceUpgradesSwitchSchema(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	if len(all) < 37 || all[35].Version != 36 || all[35].SQL != skillMaintenanceSchema || all[36].Version != 37 || all[36].SQL != WorkSwitchSchema {
		t.Fatal("switch migration must follow published skill maintenance version 36")
	}
	cfg.Migrations = all[:36]
	source := openStore(t, cfg)
	if err := source.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO skill_maintenance VALUES('operation',1,'preserved','merge',0);
INSERT INTO skill_maintenance_members VALUES('operation',1,'member',2);
INSERT INTO skill_maintenance_notices(receipt) VALUES(X'0102');`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenSQLiteReader(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	restored := storeConfig(t)
	if err := BackupSQLite(t.Context(), reader, restored.Path); err != nil {
		t.Fatal(err)
	}
	restored.Migrations = all
	restored.Epoch++
	db := openStore(t, restored)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var hash, member, receipt string
		if err := tx.QueryRowContext(t.Context(), `SELECT m.operation_hash,d.id,hex(n.receipt)
FROM skill_maintenance m JOIN skill_maintenance_members d ON d.operation_id=m.id AND d.operation_revision=m.revision
CROSS JOIN skill_maintenance_notices n`).Scan(&hash, &member, &receipt); err != nil {
			return err
		}
		if hash != "preserved" || member != "member" || receipt != "0102" {
			t.Fatalf("upgrade changed maintenance evidence: %q %q %q", hash, member, receipt)
		}
		for _, table := range []string{"work_worker_reservations", "work_switches", "work_switch_tasks", "work_switch_events"} {
			var count int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("upgrade invented switch records in %s", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
