// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestJournalRecordsAdmissionTimeAcrossRetryAndRebuild(t *testing.T) {
	j, db := testJournal(t)
	event := Transition{ID: "timed", Kind: "fixture", At: 1, RecordedAt: "2040-01-01T00:00:00Z"}
	apply := func(v *View) error { return v.Put("goal", "g", json.RawMessage(`{}`)) }
	before := time.Now()
	if err := j.Update(t.Context(), event, apply); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	read := func() string {
		t.Helper()
		var raw []byte
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT body FROM work_events WHERE id=?", event.ID).Scan(&raw)
		}); err != nil {
			t.Fatal(err)
		}
		var record struct {
			Event struct {
				RecordedAt string `json:"recorded_at"`
			}
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		return record.Event.RecordedAt
	}
	initial := read()
	at, err := time.Parse(time.RFC3339Nano, initial)
	if err != nil || at.Before(before) || at.After(after) {
		t.Fatalf("missing Core commit timestamp: %q (%v)", initial, err)
	}
	if err = j.Update(t.Context(), event, func(*View) error { t.Error("retry applied again"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err = j.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != initial {
		t.Fatalf("retry/rebuild changed recorded time: %q -> %q", initial, got)
	}
}

func TestJournalLegacyAdmissionTimeStaysUnknown(t *testing.T) {
	j, db := testJournal(t)
	const legacy = `{"event":{"id":"legacy","kind":"fixture","at":1},"changes":[{"kind":"goal","id":"old","version":1,"body":{}}]}`
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_events(id,kind,at,body) VALUES('legacy','fixture',1,?)", []byte(legacy))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.Update(t.Context(), Transition{ID: "legacy", Kind: "fixture", At: 1}, func(*View) error {
		t.Fatal("legacy retry applied again")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT body FROM work_events WHERE id='legacy'").Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	if string(raw) != legacy {
		t.Fatalf("legacy replay invented an admission timestamp: %s", raw)
	}
}
