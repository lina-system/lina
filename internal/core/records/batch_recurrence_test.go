// SPDX-License-Identifier: Apache-2.0
package records

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestBatchRecurringCompletionRecovery(t *testing.T) {
	for _, stage := range []string{"normal", "intent", "file", "checkpoint"} {
		t.Run(stage, func(t *testing.T) {
			s, root, db := setup(t)
			s.Now = func() time.Time { return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) }
			changes := []Change{
				{ID: "one", Path: "inbox/one.md", Data: todoDocument("one", "repeat: {\"rule\":\"FREQ=DAILY\",\"next\":\"2026-10-22\"}\nblocked_by: [\"two\"]\n")},
				{ID: "two", Path: "inbox/two.md", Data: todoDocument("two", "repeat: {\"rule\":\"FREQ=DAILY\",\"next\":\"2026-10-22\"}\n")},
			}
			before, err := s.CommitBatch(t.Context(), changes, actor)
			if err != nil {
				t.Fatal(err)
			}
			for i := range changes {
				changes[i].Base = before
				changes[i].Data = []byte(stringsReplaceState(changes[i].Data))
			}
			s.PublicationHook = func(at string) error {
				if at == stage {
					return errors.New("cut")
				}
				return nil
			}
			_, err = s.CommitBatch(t.Context(), changes, actor)
			if stage == "normal" && err != nil || stage != "normal" && !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
			// Reopening twice must neither advance again nor duplicate an occurrence.
			for range 2 {
				recovered, err := Open(root, db)
				if err != nil {
					t.Fatal(err)
				}
				defer recovered.Close()
				head, err := recovered.Head(t.Context())
				if err != nil || head == before {
					t.Fatal("missing batch publication", head, err)
				}
				for _, c := range changes {
					record, rev, err := recovered.Read(t.Context(), c.ID)
					if err != nil || rev != head {
						t.Fatal("partial batch", rev, err)
					}
					d, err := parseDocument(record.Data)
					if err != nil {
						t.Fatal(err)
					}
					if d.todo.State != "open" || d.todo.Repeat.Next != "2026-10-24" || d.todo.Repeat.Completed != 1 || len(d.todo.BlockedBy) != 0 {
						t.Fatalf("recurring batch did not advance exactly once: %+v", d.todo)
					}
					var n int
					err = db.Read(t.Context(), func(tx *canon.Tx) error {
						return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM work_ledger WHERE task_id=? AND json_extract(record,'$.kind')='record_occurrence' AND json_extract(record,'$.occurrence.reason')='completed' AND json_extract(record,'$.occurrence.from')='2026-10-22' AND json_extract(record,'$.occurrence.next')='2026-10-24'`, "record:"+c.ID).Scan(&n)
					})
					if err != nil || n != 1 {
						t.Fatal("missing/duplicate batch occurrence", c.ID, n, err)
					}
				}
			}
			if git(t, root, "rev-list", "--count", "HEAD") != "2" {
				t.Fatal("batch recovery created another version")
			}
		})
	}
}
