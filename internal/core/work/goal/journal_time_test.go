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
	event := Transition{ID: "timed", Kind: "fixture", At: 1}
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
