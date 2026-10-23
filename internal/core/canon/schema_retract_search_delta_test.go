// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestSelectiveRetractionSearchUpgrade(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	for i, migration := range all {
		if migration.SQL == retractSearchDeltaSchema {
			cfg.Migrations = all[:i]
			break
		}
	}
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item
 (id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
 VALUES('root','person','all','world','','도서관 root','root','local','active',0,0,'',0,'{"evidence":[]}'),
 ('child','person','all','world','','도서관 child','child','local','active',0,0,'',0,'{"evidence":[{"kind":"item","id":"root"}]}'),
 ('unrelated','person','all','world','','도서관 unrelated','unrelated','local','active',0,0,'',0,'{"evidence":[]}');
 INSERT INTO removal_applied(sequence,id,digest) VALUES(1,'forget','synthetic');
 INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id)
 VALUES('memory','root','person','all','agent','trashed',1,'{}','forget');
 UPDATE memory_item SET state='trashed' WHERE id='root';`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = all
	s = openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		var before int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_search_text").Scan(&before); err != nil {
			return err
		}
		if before != 1 {
			t.Fatalf("upgrade exposed removed memory: %d rows", before)
		}
		_, err := tx.ExecContext(t.Context(), `CREATE TEMP TABLE writes(id TEXT);
CREATE TEMP TRIGGER count_insert BEFORE INSERT ON main.memory_search_text BEGIN INSERT INTO writes VALUES(NEW.item_id); END;
CREATE TEMP TRIGGER count_delete AFTER DELETE ON main.memory_search_text BEGIN INSERT INTO writes VALUES(OLD.item_id); END;
UPDATE retract_target SET state='active' WHERE kind='memory' AND id='root';
UPDATE memory_item SET state='active' WHERE id='root';`)
		if err != nil {
			return err
		}
		var writes, unrelated, visible int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*),count(CASE WHEN id='unrelated' THEN 1 END) FROM writes").Scan(&writes, &unrelated); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_search WHERE memory_search MATCH '도서관'").Scan(&visible); err != nil {
			return err
		}
		if writes != 2 || unrelated != 0 || visible != 3 {
			t.Errorf("after upgrade recovery: writes=%d unrelated=%d FTS hits=%d", writes, unrelated, visible)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
