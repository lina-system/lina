// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestMemoryMigrationPreservesDataAndReopens(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	cfg.Migrations = all[:len(all)-1]
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
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_item").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("migration created memory")
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM assets WHERE id='kept'").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
}
