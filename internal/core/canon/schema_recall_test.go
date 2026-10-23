// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestRecallUpgradeQueuesOnlyEligibleSources(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = migrationsBefore(t, recallSchema)
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
 VALUES('old','conversation','owner','agent','person','{}','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','recorded','{"kind":"import"}','도서관');
 INSERT INTO sum_node VALUES('summary','conversation',0,'fixture','1','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z',0,'ready','도서관','',1);
 INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
 VALUES('memory','owner','all','world','','공부','공부','local','active',1,0,'',0,'{"revision":1}');
 INSERT INTO records_index VALUES('record','inbox/record.md','blob','revision');
 INSERT INTO records_text VALUES('record','owner','memo','active','도서관','공부');
 INSERT INTO import_record VALUES('observation','conversation','agent','observation',3,'unpromoted','{}',NULL);
 INSERT INTO import_record VALUES('imported-summary','conversation','agent','summary',3,'unpromoted','{}',NULL);`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		for _, table := range []string{"recall_pending", "recall_source"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 4 {
				t.Fatalf("%s=%d", table, n)
			}
		}
		var tokenizer, fallback string
		if err := tx.QueryRowContext(t.Context(), "SELECT tokenizer,fallback FROM search_index_config WHERE name='recall'").Scan(&tokenizer, &fallback); err != nil {
			return err
		}
		if tokenizer != "trigram" || fallback != "hangul_pair_words_v1" {
			t.Fatal(tokenizer, fallback)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
