// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func uidLineagesRequest(t *testing.T) Request {
	t.Helper()
	req := importRequest(t)
	for kind, path := range map[string]string{"lcm": req.Profiles[0].LCMPath, "state": req.Profiles[0].StatePath} {
		raw, err := os.ReadFile("../../../../testdata/hermes/uid-lineages-" + kind + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		sqlSource(t, path, string(raw))
	}
	return req
}

func TestImportUIDAcrossLineages(t *testing.T) {
	t.Parallel()
	req := uidLineagesRequest(t)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	rehearsal, err := p.Rehearse()
	if err != nil {
		t.Fatal(err)
	}
	if rehearsal.AmbiguousUIDRows != 6 || len(rehearsal.UIDLineageMappings) != 6 {
		t.Fatalf("ambiguous rows: %+v", rehearsal)
	}
	for i, want := range []struct{ key, root, lineage, reason string }{
		{"1", "synthetic-session", "", "multiple_root_lineages"},
		{"2", "synthetic-session", "home:synthetic-session", "session_root"},
		{"3", "synthetic-session", "", "multiple_root_lineages"},
		{"10", "synthetic-session", "home:synthetic-session", "session_root"},
		{"11", "fork", "home:fork", "session_root"},
		{"12", "reset", "", "no_root_lineage"},
	} {
		got := rehearsal.UIDLineageMappings[i]
		lineages := []string{"home:fork", "home:synthetic-session"}
		if want.key == "1" || want.key == "3" {
			lineages = []string{"home:synthetic-session", "other-home:synthetic-session"}
		}
		if got.Source != (LegacyKey{string(HermesState), "profile", "messages", want.key}) || got.SessionRoot != want.root || got.Lineage != want.lineage || got.Reason != want.reason || !reflect.DeepEqual(got.CandidateLineages, lineages) || got.EventID == "" {
			t.Errorf("mapping %d: %+v, want %+v", i, got, want)
		}
	}
	db := importStore(t)
	report, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.AddedReferences != 21 || report.AddedEvents != 7 {
		t.Fatalf("import counts: %+v", report)
	}
	if report.AmbiguousUIDRows != 6 || !reflect.DeepEqual(report.UIDLineageMappings, rehearsal.UIDLineageMappings) {
		t.Fatalf("import lost mapping report: %+v", report)
	}
	var retained RehearsalReport
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		var raw string
		if err := tx.QueryRowContext(t.Context(), "SELECT report FROM import_rehearsal WHERE batch_id=?", req.BatchID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &retained)
	}); err != nil {
		t.Fatal(err)
	}
	if retained.AmbiguousUIDRows != 6 || !reflect.DeepEqual(retained.UIDLineageMappings, rehearsal.UIDLineageMappings) {
		t.Fatalf("retained report differs: %+v", retained)
	}
	verified, err := p.Verify(t.Context(), db)
	if err != nil || verified.Rows != 21 {
		t.Fatalf("round trip: %+v, %v", verified, err)
	}
	resolve := func(system SQLiteSource, key string) string {
		t.Helper()
		ref, err := Resolve(t.Context(), db, LegacyKey{string(system), "profile", "messages", key})
		if err != nil {
			t.Fatal(err)
		}
		return ref.RecordID
	}
	// A unique session root keeps the canonical event, including compression.
	for _, mapping := range retained.UIDLineageMappings {
		if resolve(HermesState, mapping.Source.Key) != mapping.EventID {
			t.Errorf("reported destination differs: %+v", mapping)
		}
	}
	for host, lcm := range map[string]string{"2": "2", "10": "2", "11": "9"} {
		if resolve(HermesState, host) != resolve(HermesLCM, lcm) {
			t.Errorf("host %s did not map to LCM %s", host, lcm)
		}
	}
	// Unknown home tags and a reset with no binding retain each row separately.
	seen := map[string]bool{resolve(HermesLCM, "1"): true, resolve(HermesLCM, "2"): true, resolve(HermesLCM, "9"): true}
	for _, host := range []string{"1", "3", "12"} {
		id := resolve(HermesState, host)
		if seen[id] {
			t.Errorf("ambiguous host %s was collapsed", host)
		}
		seen[id] = true
	}
	again, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := again.Import(t.Context(), db)
	if err != nil || repeated.AddedEvents != 0 || repeated.AddedReferences != 0 || repeated.AddedRecords != 0 {
		t.Fatalf("reimport: %+v, %v", repeated, err)
	}
	if !reflect.DeepEqual(report.UIDLineageMappings, repeated.UIDLineageMappings) {
		t.Fatal("reimport changed mapping decisions")
	}
}

func TestImportUIDLineagesStayProfileScoped(t *testing.T) {
	t.Parallel()
	req := uidLineagesRequest(t)
	other := req.Profiles[0]
	other.ID, other.AgentID = "other-profile", "other-agent"
	req.Profiles = append(req.Profiles, other)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	report, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.AmbiguousUIDRows != 12 || report.AddedReferences != 42 || report.AddedEvents != 14 {
		t.Fatalf("profiles collapsed: %+v", report)
	}
	for i := range 6 {
		a, b := report.UIDLineageMappings[i], report.UIDLineageMappings[i+6]
		if a.Source.Instance != "profile" || b.Source.Instance != "other-profile" || a.EventID == b.EventID || a.Source.Key != b.Source.Key || a.Reason != b.Reason {
			t.Fatalf("profiles mixed: %+v, %+v", a, b)
		}
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

func TestImportUIDLineageVersions(t *testing.T) {
	t.Parallel()
	for _, matching := range []bool{true, false} {
		name := "unmatched_version"
		if matching {
			name = "matching_version"
		}
		t.Run(name, func(t *testing.T) {
			req := importRequest(t)
			sqlSource(t, req.Profiles[0].LCMPath, `
INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version)
VALUES(2,'synthetic-message-2','home:fork','canonical',1);
UPDATE host_uid_bindings SET kind='version' WHERE uid='synthetic-message-2' AND lineage_key='home:synthetic-session'`)
			// The base host row omits tool_calls. Match all payload fields only
			// in the positive case; otherwise the row must stay separate.
			if matching {
				sqlSource(t, req.Profiles[0].StatePath, `UPDATE messages SET tool_calls='[{"id":"synthetic-call","name":"synthetic-tool"}]' WHERE id=2`)
			}
			p, err := Prepare(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			db := importStore(t)
			report, err := p.Import(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			if report.AmbiguousUIDRows != 1 || len(report.UIDLineageMappings) != 1 {
				t.Fatalf("version report: %+v", report)
			}
			got := report.UIDLineageMappings[0]
			if matching {
				if got.Reason != "session_root" || got.Lineage != "home:synthetic-session" || got.EventID != keyID(LegacyKey{string(HermesLCM), "profile", "messages", "2"}) {
					t.Fatalf("version not selected: %+v", got)
				}
			} else if got.Reason != "no_matching_message" || got.Lineage != "" || got.EventID != keyID(LegacyKey{string(HermesState), "profile", "messages", "2"}) {
				t.Fatalf("unmatched version collapsed: %+v", got)
			}
			if _, err = p.Verify(t.Context(), db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
