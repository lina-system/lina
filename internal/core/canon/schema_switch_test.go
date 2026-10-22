// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestBackupFromPublishedSchemaUpgradesSwitchSchema(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	if len(all) < 39 || all[35].Version != 36 || all[35].SQL != skillMaintenanceSchema || all[36].Version != 37 || all[36].SQL != workEngineSchema || all[37].Version != 38 || all[37].SQL != recordsTodoSchema || all[38].Version != 39 || all[38].SQL != WorkSwitchSchema {
		t.Fatal("switch migration must follow published skill maintenance 36, work engine 37 and records todo 38")
	}
	cfg.Migrations = all[:38]
	source := openStore(t, cfg)
	if err := source.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO skill_maintenance VALUES('operation',1,'preserved','merge',0);
INSERT INTO skill_maintenance_members VALUES('operation',1,'member',2);
INSERT INTO skill_maintenance_notices(receipt) VALUES(X'0102');
INSERT INTO work_events(id,kind,at,body) VALUES('admit:goal','goal.admitted',1,'{"goals":[]}');
INSERT INTO work_records(kind,id,version,body) VALUES('goal','goal',1,'{"preserved":true}');`)
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
		var eventBody, recordBody string
		var recordVersion int
		if err := tx.QueryRowContext(t.Context(), `SELECT e.body,r.body,r.version
FROM work_events e CROSS JOIN work_records r
WHERE e.id='admit:goal' AND r.kind='goal' AND r.id='goal'`).Scan(&eventBody, &recordBody, &recordVersion); err != nil {
			return err
		}
		if eventBody != `{"goals":[]}` || recordBody != `{"preserved":true}` || recordVersion != 1 {
			t.Fatalf("upgrade changed work engine evidence: %q %q %d", eventBody, recordBody, recordVersion)
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
