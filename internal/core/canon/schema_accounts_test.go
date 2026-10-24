// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestAccountsMigrationPreservesExistingDataAndReopens(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == accountsSchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("accounts migration not appended")
	}
	cfg.Migrations = all[:index]
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO assets(id,area,observed_path,revision,person_id,agent_id) VALUES('kept','user','item',1,'person','agent')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = all
	cfg.Epoch++
	s = openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO accounts VALUES('account',?)", []byte(`{"Status":"needs_sign_in"}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	s = openStore(t, cfg)
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var record []byte
		var revision int
		if err := tx.QueryRowContext(t.Context(), "SELECT record FROM accounts WHERE id='account'").Scan(&record); err != nil {
			return err
		}
		if string(record) != `{"Status":"needs_sign_in"}` {
			t.Fatal("account metadata lost")
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT revision FROM assets WHERE id='kept'").Scan(&revision); err != nil {
			return err
		}
		if revision != 1 {
			t.Fatal("prior data changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
