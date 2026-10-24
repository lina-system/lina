// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"errors"
	"testing"
)

func TestStoreContractRemovesLegacyIndexes(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		for _, name := range []string{"ledger_search", "ledger_search_text", "ledger_search_bigram", "ledger_search_pending", "memory_search", "memory_search_text", "memory_search_bigram", "records_fts", "records_korean_fts", "records_korean_bigram"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name=?", name).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Errorf("legacy index remains: %s", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryCitationConstraints(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), ledgerFixture+`
INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
 SELECT 'second',conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body FROM conv_event WHERE id='first';
INSERT INTO sum_node VALUES
 ('leaf','conversation',0,'model','v1','','','',0,'ready','summary','',1),
 ('root','conversation',1,'model','v1','','','',0,'ready','summary','',1),
 ('foreign','other-conversation',0,'model','v1','','','',0,'ready','summary','',1);
INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,precision,role) VALUES
 ('summary','leaf','1',0,'event','first','0','event','covers'),
 ('summary','root','1',0,'summary','leaf','1','none','cites');`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"delete covered leaf": "DELETE FROM sum_node WHERE id='leaf'",
		"delete parent":       "DELETE FROM sum_node WHERE id='root'",
		"foreign event": `INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,precision,role)
 VALUES('summary','foreign','1',0,'event','first','0','event','covers')`,
		"memory as summary source": `INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,precision,role)
 VALUES('summary','root','1',1,'memory','unrelated','1','none','cites')`,
		"invalid event role": `INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,precision,role)
 VALUES('summary','root','1',1,'event','second','0','event','cites')`,
		"cyclic child": `INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,precision,role)
 VALUES('summary','leaf','1',1,'summary','root','1','none','cites')`,
	} {
		t.Run(name, func(t *testing.T) {
			accepted := false
			_ = db.Write(t.Context(), func(tx *Tx) error {
				if _, err := tx.ExecContext(t.Context(), statement); err != nil {
					return err
				}
				accepted = true
				return errors.New("rollback constraint probe")
			})
			if accepted {
				t.Fatal("invalid summary mutation accepted")
			}
		})
	}
}
