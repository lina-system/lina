// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestMemoryMigrationPreservesDataAndReopens(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == memorySchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("missing appended memory migration")
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
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_item").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("migration created memory")
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM assets WHERE id='kept'").Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("prior data lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record) VALUES('memory','person','all','person','person','kept','kept','local','active',0,0,'',0,'{}');
   INSERT INTO memory_revision(item_id,revision,record) VALUES('memory',1,'{}');`)
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
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_revision WHERE item_id='memory' AND revision=1").Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("committed memory lost on reopen")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
