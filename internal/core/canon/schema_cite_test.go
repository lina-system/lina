// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCitationMigrationPreservesRevisionPointers(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	cfg.Migrations = all
	for i, m := range all {
		if strings.Contains(m.SQL, "CREATE TABLE cite (") {
			cfg.Migrations = all[:i]
			break
		}
	}
	db := openStore(t, cfg)
	err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), ledgerFixture+`
INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body) VALUES('copy','conversation','person','agent','person','{}','','','observed','{}','copy');
INSERT INTO sum_node VALUES('leaf','conversation',0,'model','v1','','','',0,'ready','summary','',1),('root','conversation',1,'model','v1','','','',0,'ready','summary','',2);
INSERT INTO sum_cover VALUES('leaf','first',0,''),('leaf','copy',1,'replay');
INSERT INTO sum_edge VALUES('root','leaf',0);
CREATE TABLE IF NOT EXISTS memory_person_brief(person TEXT NOT NULL,agent TEXT NOT NULL,generation INTEGER NOT NULL,record BLOB NOT NULL,PRIMARY KEY(person,agent));
INSERT INTO memory_person_brief VALUES('person','agent',3,'{"sentences":[{"text":"derived","citations":[{"kind":"item","id":"m","revision":2},{"kind":"event","id":"first","precision":"event"}]}]}');
INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
VALUES('m','person','all','world','','fact','fact','local','active',0,0,'',0,'{}');`)
		if err != nil {
			return err
		}
		// The first pointer persists, another is removed and later re-added.
		evidence := []map[string]any{{"kind": "event", "id": "first", "precision": "event", "quote": "original"}, {"kind": "material", "id": "asset", "revision": 7, "precision": "session"}}
		for rev := 1; rev <= 4; rev++ {
			refs := append([]map[string]any{}, evidence...)
			if rev == 2 {
				refs = append(refs, map[string]any{"kind": "work_result", "id": "work", "precision": "none"})
			}
			if rev == 3 {
				refs = refs[:1]
			}
			if rev == 4 {
				refs = append(refs, map[string]any{"kind": "external", "id": "uri", "precision": "none"}, map[string]any{"kind": "item", "id": "other", "revision": 2, "precision": "none"})
			}
			raw, _ := json.Marshal(map[string]any{"revision": rev, "evidence": refs})
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO memory_revision(item_id,revision,record) VALUES('m',?,?)", rev, raw); err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "UPDATE memory_item SET record=? WHERE id='m'", raw); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = all
	db = openStore(t, cfg)
	if err = db.Read(t.Context(), func(tx *Tx) error {
		var count int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM cite").Scan(&count); err != nil {
			return err
		}
		if count != 11 {
			t.Errorf("citation intervals=%d, want 11 (3 summary + 6 memory + 2 brief)", count)
		}
		for rev, want := range map[int]int{1: 2, 2: 3, 3: 1, 4: 4} {
			if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM cite WHERE view_kind='memory' AND view_id='m' AND CAST(added_revision AS INTEGER)<=? AND (dropped_revision IS NULL OR CAST(dropped_revision AS INTEGER)>?)`, rev, rev).Scan(&count); err != nil {
				return err
			}
			if count != want {
				t.Errorf("revision %d: %d pointers, want %d", rev, count, want)
			}
		}
		rows, err := tx.QueryContext(t.Context(), `SELECT view_kind||'|'||view_id||'|'||added_revision||'|'||coalesce(dropped_revision,'-')||'|'||ordinal||'|'||target_kind||'|'||target_id||'|'||target_revision||'|'||precision||'|'||role FROM cite ORDER BY view_kind,view_id,ordinal,CAST(added_revision AS INTEGER)`)
		if err != nil {
			return err
		}
		var got []string
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				return err
			}
			got = append(got, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		want := []string{
			"memory|m|1|-|0|event|first|0|event|cites",
			"memory|m|1|3|1|material|asset|7|session|cites",
			"memory|m|4|-|1|material|asset|7|session|cites",
			"memory|m|2|3|2|work_result|work|0|none|cites",
			"memory|m|4|-|2|external|uri|0|none|cites",
			"memory|m|4|-|3|memory|other|2|none|cites",
			`person_brief|["person","agent"]|3|-|0|memory|m|2|none|cites`,
			`person_brief|["person","agent"]|3|-|1|event|first|0|event|cites`,
			"summary|leaf|1|-|0|event|first|0|event|covers",
			"summary|leaf|1|-|1|event|copy|0|event|replay",
			"summary|root|2|-|0|summary|leaf|1|none|cites",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("citation pointers\ngot: %q\nwant: %q", got, want)
		}
		var start, end int
		if err := tx.QueryRowContext(t.Context(), "SELECT span_start,span_end FROM cite WHERE view_kind='memory' AND target_id='first'").Scan(&start, &end); err != nil {
			return err
		}
		if start != 0 || end != len("original") {
			t.Errorf("quote span=(%d,%d)", start, end)
		}
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_table_info('cite') WHERE name IN ('text','quote','body','record')`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Error("citation stores copied text")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
