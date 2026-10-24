// SPDX-License-Identifier: Apache-2.0
package work

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestRecordOccurrenceDoesNotCreateWorkCard(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	task := taskFixture()
	if err := s.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	event := Event{Kind: "record_occurrence", TaskID: "record:todo", Key: "record-occurrence:revision", Occurrence: &canon.RecordOccurrence{Record: "todo", At: "2026-01-01T00:00:00Z", From: "2026-01-01", Next: "2026-01-02", Reason: "completed"}}
	raw, _ := json.Marshal(event)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", event.TaskID, event.Key, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), event.TaskID); err != ErrNotFound {
		t.Fatal("invented work card", err)
	}
	events, err := s.Ledger(t.Context(), event.TaskID)
	if err != nil || len(events) != 1 || events[0].Occurrence == nil {
		t.Fatal(events, err)
	}
}
