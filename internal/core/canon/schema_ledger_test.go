// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"reflect"
	"testing"
)

const ledgerFixture = `INSERT INTO conv_event
 (id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
 VALUES('first','conversation','person','agent','person','{"kind":"tui"}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','observed','{"kind":"native"}','original');
 INSERT INTO ev_rel(event_id,target_id,kind,ordinal) VALUES('first','first','view_of',0);
 INSERT INTO ev_span(event_id,start,end,sensitivity) VALUES('first',0,3,'personal_id');`

func TestLedgerMigrationPreservesPriorData(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == ledgerSchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("ledger migration was not appended")
	}
	cfg.Migrations = all[:index]
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO inbox(conversation,source,native_id,body) VALUES('kept','client','input','original')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	cfg.Migrations = all
	s = openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), ledgerFixture); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var body string
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM inbox WHERE conversation='kept'").Scan(&body); err != nil {
			return err
		}
		if body != "original" {
			t.Fatalf("prior data: %q", body)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerFieldsAndImmutableRows(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	s := openStore(t, cfg)
	columns := map[string][]string{
		"conv_event": {"id", "conversation_id", "parent_id", "person_id", "agent_id", "speaker_kind", "surface", "scaffold_kind", "scaffold_id", "occurred_at", "recorded_at", "time_quality", "origin", "replay_of", "body", "material_id", "material_revision"},
		"ev_rel":     {"event_id", "target_id", "kind", "ordinal"},
		"ev_span":    {"event_id", "start", "end", "sensitivity"},
	}
	for table, want := range columns {
		err := s.Read(t.Context(), func(tx *Tx) error {
			rows, err := tx.QueryContext(t.Context(), "SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
			if err != nil {
				return err
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return err
				}
				got = append(got, name)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s fields: got %v, want %v", table, got, want)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), ledgerFixture); return err }); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"UPDATE conv_event SET body='changed' WHERE id='first'",
		"UPDATE ev_rel SET ordinal=1",
		"UPDATE ev_span SET sensitivity='third_party'",
		"DELETE FROM conv_event", "DELETE FROM ev_rel", "DELETE FROM ev_span",
		"INSERT OR REPLACE INTO conv_event SELECT * FROM conv_event",
		"INSERT OR REPLACE INTO ev_rel SELECT * FROM ev_rel",
		"INSERT OR REPLACE INTO ev_span SELECT * FROM ev_span",
	} {
		if err := s.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), statement); return err }); err == nil {
			t.Errorf("ledger mutation succeeded: %s", statement)
		}
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	s = openStore(t, cfg)
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var body string
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM conv_event WHERE id='first'").Scan(&body); err != nil {
			return err
		}
		if body != "original" {
			t.Fatalf("reopened body = %q", body)
		}
		for _, table := range []string{"ev_rel", "ev_span"} {
			var count int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Fatalf("%s rows = %d", table, count)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
