// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestCurrentHermesLayout(t *testing.T) {
	req := importRequest(t)
	sqlSource(t, req.Profiles[0].StatePath, string(fixture(t, "delivery.sql")))
	sqlSource(t, req.Profiles[0].LCMPath, string(fixture(t, "current-kinds.sql"))+`; DELETE FROM host_uid_bindings WHERE store_id=2 AND kind='canonical'`)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	rehearsal, err := p.Rehearse()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rehearsal.Inventory)
	if err != nil {
		t.Fatal(err)
	}
	var inventory []struct {
		Table, Omitted string
		Rows           int
		Kinds          map[string]int
	}
	if err = json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, table := range inventory {
		switch table.Table {
		case "delivery_obligations":
			if table.Rows != 1 || table.Omitted == "" {
				t.Fatalf("delivery inventory: %+v", table)
			}
			seen[table.Table] = true
		case "host_uid_bindings":
			if table.Kinds["alias_candidate"] != 1 {
				t.Fatalf("binding inventory: %+v", table)
			}
			seen[table.Table] = true
		case "message_relations":
			if table.Kinds["composite"] != 2 {
				t.Fatalf("relation inventory: %+v", table)
			}
			seen[table.Table] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("missing inventory: %s", raw)
	}
	db := importStore(t)
	assertCount(t, db, "import_record", 0)
	report, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.AddedEvents != 4 {
		t.Fatalf("candidate merged host identities: %+v", report)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil }); err != nil {
		t.Fatal(err)
	}
	verified, err := p.Verify(t.Context(), db)
	if err != nil || verified.Rows != 13 {
		t.Fatalf("round trip: %+v %v", verified, err)
	}
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM import_link WHERE kind='composite'").Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			t.Errorf("composite members=%d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range p.Keys() {
		row, err := Reverse(t.Context(), db, key)
		if err != nil {
			t.Fatal(err)
		}
		if key.Kind == "message_relations" && (key.Key == "2" || key.Key == "3") && row.text("kind") != "composite" {
			t.Fatal("lost composite source kind")
		}
	}
	again, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := again.Import(t.Context(), db)
	if err != nil || repeat.AddedReferences != 0 || repeat.AddedEvents != 0 {
		t.Fatalf("repeat: %+v %v", repeat, err)
	}
}

func TestDeliveryLayoutGate(t *testing.T) {
	for _, change := range []string{"", "ALTER TABLE delivery_obligations DROP COLUMN adapter_profile", "CREATE TABLE unknown_table(id INTEGER)", "ALTER TABLE delivery_obligations ADD COLUMN unknown_column TEXT", "ALTER TABLE delivery_obligations DROP COLUMN state"} {
		t.Run(change, func(t *testing.T) {
			source, db := hermesFixture(t, "state")
			if _, err := db.Exec(string(fixture(t, "delivery.sql")) + ";" + change); err != nil {
				t.Fatal(err)
			}
			err := SnapshotSQLite(t.Context(), source, filepath.Join(t.TempDir(), "snapshot.db"), HermesState)
			accepted := change == "" || strings.Contains(change, "DROP COLUMN adapter_profile")
			if accepted && err != nil || !accepted && !errors.Is(err, ErrSchema) {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}

func TestSnapshotChecksAreNamed(t *testing.T) {
	for _, query := range []string{
		"UPDATE host_uid_bindings SET binding_version=2",
		"UPDATE host_uid_bindings SET kind='unknown_kind'",
		"UPDATE message_relations SET kind='unknown_kind'",
		"UPDATE summary_nodes SET source_ids='invalid-json'",
		"UPDATE summary_nodes SET source_type='unknown_kind'",
		"UPDATE summary_nodes SET source_type='nodes',source_ids='[1]'",
	} {
		t.Run(query, func(t *testing.T) {
			req := importRequest(t)
			sqlSource(t, req.Profiles[0].LCMPath, query)
			_, err := Prepare(t.Context(), req)
			requireSnapshotCheck(t, err)
		})
	}
	t.Run("confirmation", func(t *testing.T) { _, err := Prepare(t.Context(), Request{}); requireSnapshotCheck(t, err) })
	t.Run("speaker map", func(t *testing.T) {
		_, err := ClassifySpeakers(t.Context(), SpeakerProfile{})
		requireSnapshotCheck(t, err)
	})
	t.Run("span", func(t *testing.T) {
		_, err := Project([]byte("test"), []Span{{Start: 0, End: 5, Kind: "secret"}}, false)
		requireSnapshotCheck(t, err)
	})
	t.Run("typed row", func(t *testing.T) {
		requireSnapshotCheck(t, checkRow(Row{"value": {Kind: "integer", Data: "secret-invalid-number"}}))
	})
}

func requireSnapshotCheck(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrSnapshot) || !strings.HasPrefix(err.Error(), ErrSnapshot.Error()+": ") || strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrSnapshot.Error()+": ")) == "" || strings.Contains(err.Error(), "secret-invalid-number") {
		t.Fatalf("snapshot refusal needs a safe check name: %v", err)
	}
}
