// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestTimerMigrationPreservesCompletions(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == TimersSchema {
			index = i
		}
	}
	if index < 1 {
		t.Fatal("timer migration missing")
	}
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_completions(id,task_id,attempt,head,record) VALUES('event','task','attempt','head',X'7B7D')")
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
		var count int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_completions WHERE id='event' AND task_id='task' AND attempt='attempt' AND head='head' AND record=X'7B7D'").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("completion changed")
		}
		for _, table := range []string{"timer", "timer_fire"} {
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("invented timer", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
