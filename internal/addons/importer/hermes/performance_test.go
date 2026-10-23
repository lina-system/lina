// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestApplyRehearsesOncePerPlan(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "after-preview"}[preview], func(t *testing.T) {
			req := importRequest(t)
			second := req.Profiles[0]
			second.ID = "second"
			req.Profiles = append(req.Profiles, second)
			p, err := Prepare(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { p.Close() })
			if p.Conversations() != 2 {
				t.Fatal("fixture must exercise multiple conversations")
			}
			calls := 0
			compute := p.rehearse
			p.rehearse = func() (RehearsalReport, error) {
				calls++
				return compute()
			}
			want, err := p.rehearseSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if preview {
				got, err := p.Rehearse()
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("preview differs: %v", err)
				}
				// A caller cannot mutate the report retained for adoption.
				got.Speakers[0].Events = nil
				got.Tables[0].MappedRows = -1
			}
			db := importStore(t)
			rollback := errors.New("rollback after first conversation")
			err = db.Write(t.Context(), func(tx *canon.Tx) error {
				if _, err := p.Apply(t.Context(), tx, 0); err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			assertCount(t, db, "import_rehearsal", 0)
			if _, err := p.Import(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Import(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Errorf("rehearsal calls = %d, want 1 across preview, conversations and retry", calls)
			}
			assertCount(t, db, "import_rehearsal", 1)
			assertCount(t, db, "import_event", 6)
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				var encoded string
				if err := tx.QueryRowContext(t.Context(), "SELECT report FROM import_rehearsal WHERE batch_id=?", req.BatchID).Scan(&encoded); err != nil {
					return err
				}
				var got any
				if err := json.Unmarshal([]byte(encoded), &got); err != nil {
					return err
				}
				wantJSON, err := json.Marshal(want)
				if err != nil {
					return err
				}
				var wantValue any
				if err := json.Unmarshal(wantJSON, &wantValue); err != nil {
					return err
				}
				if !reflect.DeepEqual(got, wantValue) {
					t.Error("stored rehearsal differs from snapshot report")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Verify(t.Context(), db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRehearsalFailureIsRetainedWithoutWrites(t *testing.T) {
	p, err := Prepare(t.Context(), importRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	calls := 0
	p.rehearse = func() (RehearsalReport, error) {
		calls++
		return RehearsalReport{}, ErrConflict
	}
	db := importStore(t)
	for range 2 {
		if _, err := p.Rehearse(); !errors.Is(err, ErrConflict) {
			t.Fatalf("preview error = %v", err)
		}
		if _, err := p.Import(t.Context(), db); !errors.Is(err, ErrConflict) {
			t.Fatalf("import error = %v", err)
		}
	}
	if calls != 1 {
		t.Errorf("failed rehearsal calls = %d, want 1", calls)
	}
	for _, table := range []string{"import_batch", "import_rehearsal", "import_record", "import_event"} {
		assertCount(t, db, table, 0)
	}
}

func TestRehearsalPreviewDoesNotChangeInventoryKinds(t *testing.T) {
	p, err := Prepare(t.Context(), importRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	preview, err := p.Rehearse()
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for _, table := range preview.Inventory {
		for kind := range table.Kinds {
			table.Kinds[kind] = -1
			changed = true
		}
	}
	if !changed {
		t.Fatal("fixture must include binding kind counts")
	}
	again, err := p.Rehearse()
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("preview mutation changed the next rehearsal report")
	}
}

func TestImportUIDLookupPreservesLineage(t *testing.T) {
	for _, tc := range []struct {
		name, lineage, extra string
		matched, ambiguous   bool
	}{
		{"exact", "synthetic-session", "", true, false},
		{"qualified", "home:synthetic-session", "", true, false},
		{"nested-prefix", "home:profile:synthetic-session", "", true, false},
		{"unrelated", "other-session", "", false, false},
		{"suffix-without-separator", "notsynthetic-session", "", false, false},
		{"same-uid-other-lineage", "synthetic-session", "other-session", true, false},
		{"ambiguous", "synthetic-session", "home:synthetic-session", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := importRequest(t)
			sqlSource(t, req.Profiles[0].LCMPath, "UPDATE host_uid_bindings SET lineage_key='"+tc.lineage+"'")
			if tc.extra != "" {
				sqlSource(t, req.Profiles[0].LCMPath, "INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version) VALUES(1,'synthetic-message-1','"+tc.extra+"','canonical',1)")
			}
			p, err := Prepare(t.Context(), req)
			if tc.ambiguous {
				if !errors.Is(err, ErrSnapshot) {
					t.Fatalf("ambiguous lineage: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { p.Close() })
			db := importStore(t)
			if _, err := p.Import(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			lcm, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "1"})
			if err != nil {
				t.Fatal(err)
			}
			host, err := Resolve(t.Context(), db, LegacyKey{string(HermesState), "profile", "messages", "1"})
			if err != nil {
				t.Fatal(err)
			}
			if (lcm.RecordID == host.RecordID) != tc.matched {
				t.Fatalf("matched = %v, want %v", lcm.RecordID == host.RecordID, tc.matched)
			}
			if _, err := p.Verify(t.Context(), db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
