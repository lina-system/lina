// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestResultsMigrationKeepsExistingConversation(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == ResultsSchema+PlanningSchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("results migration absent")
	}
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), ledgerFixture); return err }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = all
	cfg.Epoch++
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var body string
		if err := tx.QueryRowContext(t.Context(), `SELECT body FROM conv_event WHERE id='first'`).Scan(&body); err != nil {
			return err
		}
		if body != "original" {
			t.Error("migration changed existing conversation")
		}
		for _, table := range []string{"result_heads", "result_saves"} {
			var count int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Error("migration fabricated results")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
