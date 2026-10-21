// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestRetractionMigrationPreservesOperationsOnUpgrade(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	s := openStore(t, cfg)
	err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO removal_applied(sequence,id,digest) VALUES(1,'operation','digest');
INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id) VALUES('memory','item','person','all','agent','erased',10,'{}','operation');
UPDATE retract_meta SET epoch=epoch+1 WHERE singleton=1;`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, cfg)
	var epoch int64
	if err := s.Read(t.Context(), func(tx *Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch)
	}); err != nil || epoch != 1 {
		t.Fatalf("epoch=%d: %v", epoch, err)
	}
}
