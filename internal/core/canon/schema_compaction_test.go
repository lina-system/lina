// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestSummaryTablesPublishAtomically(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO sum_node
  (id,conversation_id,level,model,prompt_version,built_at,start_at,end_at,epoch,status,text,dropped,revision)
  VALUES('summary','conversation',0,'model','v1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',0,'ready','summary','',1);
  INSERT INTO sum_cover(node_id,event_id,ordinal,reason) VALUES('summary','missing-event',0,'');`)
		return err
	})
	if err == nil {
		t.Fatal("missing event coverage committed")
	}
	if err := db.Read(t.Context(), func(tx *Tx) error {
		for _, table := range []string{"sum_node", "sum_edge", "sum_cover"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatalf("partial publication in %s: %d", table, n)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryMigrationPreservesLedgerAndRevocationEpoch(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	for i, m := range all {
		if m.SQL == compactionSchema {
			cfg.Migrations = all[:i]
			break
		}
	}
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), ledgerFixture+"UPDATE retract_meta SET epoch=9;")
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
		var body string
		var epoch int
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM conv_event WHERE id='first'").Scan(&body); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT epoch FROM retract_meta").Scan(&epoch); err != nil {
			return err
		}
		if body != "original" || epoch != 9 {
			t.Fatalf("upgrade lost state: %q %d", body, epoch)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
