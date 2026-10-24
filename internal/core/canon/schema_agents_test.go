// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestAgentMigrationPreservesPriorDataAndReopens(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == agentsSchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("missing appended agents migration")
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
		_, err := tx.ExecContext(t.Context(), `INSERT INTO agents VALUES('agent','Name','',1,'{}',0); INSERT INTO agent_revisions VALUES('agent',1,'commit','person'); INSERT INTO agent_preparations VALUES('turn','agent',1);`)
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
		var rev int
		if err := tx.QueryRowContext(t.Context(), "SELECT revision FROM agent_preparations WHERE turn_id='turn'").Scan(&rev); err != nil {
			return err
		}
		if rev != 1 {
			t.Fatal("preparation lost")
		}
		var id string
		if err := tx.QueryRowContext(t.Context(), "SELECT id FROM assets WHERE id='kept'").Scan(&id); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
