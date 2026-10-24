// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestBackupFromPublishedSchemaUpgradesSwitchSchema(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	published := []Migration{
		{Version: 36, SQL: skillMaintenanceSchema},
		{Version: 37, SQL: workEngineSchema},
		{Version: 38, SQL: recordsTodoSchema},
		{Version: 39, SQL: workEventSequenceSchema},
		{Version: 40, SQL: WorkCompletionsSchema},
		{Version: 41, SQL: TimersSchema},
		{Version: 42, SQL: startersSchema},
		{Version: 43, SQL: WorkerChoiceSchema},
		{Version: 44, SQL: namingSchema},
		{Version: 45, SQL: recordOperationsSchema},
		{Version: 46, SQL: WorkSwitchSchema},
	}
	for _, want := range published {
		if len(all) < want.Version || all[want.Version-1] != want {
			t.Fatalf("published migration %d changed or switch did not follow it", want.Version)
		}
	}
	cfg.Migrations = migrationsBefore(t, WorkSwitchSchema)
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
