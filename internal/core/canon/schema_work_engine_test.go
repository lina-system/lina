// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestWorkLedgerUpgradePreservesPublishedPresetEffects(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	// Version 35 is already published by dev. Exercise its actual tables and
	// retained records before appending the work journal at version 36.
	cfg.Migrations = all[:35]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `
INSERT INTO authority_delegations(task_id,person,source,source_task,receiver,limits)
 VALUES('task','person','source','parent','receiver',X'7B7D');
INSERT INTO coding_managers(id,person,owner,unit,conversation)
 VALUES('manager','person','owner','unit','conversation');
INSERT INTO coding_assignments(id,manager,record)
 VALUES('assignment','manager',X'7B7D');`)
		return err
	}); err != nil {
		t.Fatalf("published migration 35 is not the preset effects schema: %v", err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations, cfg.Epoch = all, cfg.Epoch+1
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var version, events, records int
		if err := tx.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version != 36 {
			t.Fatalf("goal journal version = %d, want 36 after published preset effects", version)
		}
		var person, manager, conversation string
		var limits, record []byte
		if err := tx.QueryRowContext(t.Context(), `SELECT d.person,d.limits,a.manager,a.record,m.conversation
FROM authority_delegations d, coding_assignments a JOIN coding_managers m ON m.id=a.manager
WHERE d.task_id='task' AND a.id='assignment'`).Scan(&person, &limits, &manager, &record, &conversation); err != nil {
			return err
		}
		if person != "person" || string(limits) != "{}" || manager != "manager" || string(record) != "{}" || conversation != "conversation" {
			t.Fatal("goal journal upgrade changed preset effects records")
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_events").Scan(&events); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_records").Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLedgerUpgradePreservesExistingCanon(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == workEngineSchema {
			index = i
		}
	}
	if index < 1 {
		t.Fatal("work migration must append to published catalog")
	}
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), ledgerFixture)
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
		var body string
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM conv_event WHERE id='first'").Scan(&body); err != nil {
			return err
		}
		if body != "original" {
			t.Fatalf("migration changed conversation: %q", body)
		}
		var events int
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_events").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLedgerMigrationAndAtomicProjection(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	const event = `INSERT INTO work_events(id,kind,at,body) VALUES('admit:goal','goal.admitted',1,'{"goals":[]}');`
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), event+`INSERT INTO work_records(kind,id,version,body) VALUES('goal','goal',1,'{}');`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE work_events SET body='{}'`,
		`DELETE FROM work_events`,
		`INSERT OR REPLACE INTO work_events SELECT * FROM work_events`,
		`INSERT INTO work_events(id,kind,at,body) VALUES('invalid','node.changed',1,'bad-json')`,
	} {
		if err := db.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), stmt); return err }); err == nil {
			t.Fatalf("accepted invalid ledger mutation: %s", stmt)
		}
	}
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE work_records SET version=2,body='{"changed":true}' WHERE id='goal';`+event)
		return err
	}); err == nil {
		t.Fatal("duplicate event accepted")
	}
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var version int
		if err := tx.QueryRowContext(t.Context(), `SELECT version FROM work_records WHERE id='goal'`).Scan(&version); err != nil {
			return err
		}
		if version != 1 {
			t.Fatal("projection changed without its committed event")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
