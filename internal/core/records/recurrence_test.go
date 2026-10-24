// SPDX-License-Identifier: Apache-2.0
package records

import (
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"strings"
	"testing"
	"time"
)

func TestExhaustedRecurringCompletionLedger(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		name := "normal"
		if interrupted {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) {
			s, root, db := setup(t)
			now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
			s.Now = func() time.Time { return now }
			c := Change{ID: "exhausted", Path: "inbox/exhausted.md", Data: todoDocument("exhausted", "repeat: {\"rule\":\"FREQ=DAILY;COUNT=1\",\"next\":\"2020-01-01\"}\n")}
			commit(t, s, c)
			if err := s.AdvanceRecurring(t.Context(), actor, now); err != nil {
				t.Fatal(err)
			}
			r, rev, err := s.Read(t.Context(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := parseDocument(r.Data)
			if err != nil || before.state != "open" || !before.todo.Repeat.Exhausted {
				t.Fatal("expected an exhausted open series", before, err)
			}
			c.Base, c.Data = rev, []byte(strings.Replace(string(r.Data), `state: "open"`, `state: "completed"`, 1))
			if interrupted {
				s.PublicationHook = func(stage string) error {
					if stage == "file" {
						return errors.New("interrupted completion")
					}
					return nil
				}
			}
			_, err = s.Commit(t.Context(), c, actor)
			if (!interrupted && err != nil) || (interrupted && !errors.Is(err, ErrUncertain)) {
				t.Fatal("completion publication", err)
			}
			recovered, err := Open(root, db)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			r, rev, err = recovered.Read(t.Context(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := parseDocument(r.Data)
			if err != nil || after.state != "completed" || *after.todo.Repeat != *before.todo.Repeat {
				t.Fatal("completion changed exhausted schedule", after, err)
			}
			// Repeating the saved write and editing its note are not completions.
			c.Base, c.Data = rev, r.Data
			c.Base = commit(t, recovered, c)
			c.Data = append(c.Data, []byte("\nCompletion note.\n")...)
			commit(t, recovered, c)
			if err = recovered.Rebuild(t.Context()); err != nil {
				t.Fatal(err)
			}
			var events, completed int
			err = db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*),coalesce(sum(json_extract(record,'$.occurrence.reason')='completed' AND json_extract(record,'$.occurrence.exhausted')=1 AND json_extract(record,'$.occurrence.from')='2020-01-01' AND json_extract(record,'$.occurrence.next')='2020-01-01'),0) FROM work_ledger WHERE task_id='record:exhausted' AND json_extract(record,'$.kind')='record_occurrence'").Scan(&events, &completed)
			})
			if err != nil || events != 2 || completed != 1 {
				t.Fatalf("exhausted completion ledger: events=%d completed=%d, want 2 and 1: %v", events, completed, err)
			}
		})
	}
}

func TestRepeatRefusesUnsupportedRules(t *testing.T) {
	t.Parallel()
	for _, rule := range []string{"FREQ=MINUTELY", "FREQ=DAILY;UNKNOWN=1", "FREQ=DAILY;COUNT=2;UNTIL=20261231T000000Z", "FREQ=MONTHLY;BYMONTHDAY=0"} {
		if err := (Todo{Repeat: &Repeat{Rule: rule, Next: "2026-01-31"}}).Validate(); err == nil {
			t.Errorf("unsupported recurrence saved: %s", rule)
		}
	}
}

func TestRecurringCompletionAdvancesFuture(t *testing.T) {
	t.Parallel()
	s, _, db := setup(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	c := Change{ID: "repeat", Path: "inbox/repeat.md", Data: todoDocument("repeat", "repeat: {\"rule\":\"FREQ=MONTHLY\",\"next\":\"2020-01-31\"}\n")}
	c.Base = commit(t, s, c)
	c.Data = []byte(stringsReplaceState(c.Data))
	c.Base = commit(t, s, c)
	r, _, err := s.Read(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := parseDocument(r.Data)
	if err != nil {
		t.Fatal(err)
	}
	next, err := time.Parse(time.DateOnly, d.todo.Repeat.Next)
	if err != nil || !next.After(now) || d.todo.State != "open" {
		t.Fatalf("completion did not advance a single live record to the future: %+v, %v", d.todo, err)
	}
	var n int
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_ledger WHERE task_id='record:repeat' AND json_extract(record,'$.kind')='record_occurrence' AND json_extract(record,'$.occurrence.reason')='completed'").Scan(&n)
	}); err != nil || n != 1 {
		t.Fatal("completion missing from work ledger", n, err)
	}
}

func TestClaimedRecurringCompletionStaysVisible(t *testing.T) {
	t.Parallel()
	s, _, db := setup(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	data := todoDocument("claim", "repeat: {\"rule\":\"FREQ=MONTHLY\",\"next\":\"2020-01-31\"}\n")
	data = []byte(strings.Replace(string(data), "state: open", "state: claimed", 1))
	revision := commit(t, s, Change{ID: "claim", Path: "inbox/claim.md", Data: data})
	if err := s.AdvanceRecurring(t.Context(), Actor{Person: "person", Agent: "agent"}, now); err != nil {
		t.Fatal(err)
	}
	record, current, err := s.Read(t.Context(), "claim")
	if err != nil || current != revision || string(record.Data) != string(data) {
		t.Fatalf("unverified occurrence disappeared: %s %v", record.Data, err)
	}
	var occurrences int
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_ledger WHERE task_id='record:claim'").Scan(&occurrences)
	})
	if err != nil || occurrences != 0 {
		t.Fatalf("claim became an occurrence: %d %v", occurrences, err)
	}
}

func stringsReplaceState(data []byte) string {
	return strings.Replace(string(data), "state: open", "state: completed", 1)
}

func TestRecurrenceCompletionModesAndExhaustion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	for _, fromCompletion := range []bool{false, true} {
		v := Todo{State: "completed", BlockedBy: []string{"prior"}, Checklist: []ChecklistItem{{Text: "Step", Checked: true}}, Repeat: &Repeat{Rule: "FREQ=DAILY;INTERVAL=3", Next: "2026-01-01", FromCompletion: fromCompletion}}
		changed, err := advanceTodo(&v, now, true)
		want := "2026-01-13"
		if err != nil || !changed || v.Repeat.Next != want || len(v.BlockedBy) != 0 || v.Checklist[0].Checked || v.State != "open" {
			t.Fatal(v, err)
		}
		if fromCompletion && v.Repeat.Start != "2026-01-10" {
			t.Fatal("completion anchor", v.Repeat)
		}
	}
	v := Todo{State: "open", Repeat: &Repeat{Rule: "FREQ=DAILY;COUNT=2", Next: "2026-01-01"}}
	if _, err := advanceTodo(&v, now, false); err != nil || !v.Repeat.Exhausted || v.State != "open" {
		t.Fatal("absence invented completion", v, err)
	}
	v = Todo{State: "completed", Repeat: &Repeat{Rule: "FREQ=DAILY;INTERVAL=3;COUNT=2", Next: "2026-01-01", FromCompletion: true}}
	if _, err := advanceTodo(&v, now, true); err != nil || v.Repeat.Exhausted || v.Repeat.Next != "2026-01-13" {
		t.Fatal(v, err)
	}
	v.State = "completed"
	if _, err := advanceTodo(&v, now.AddDate(0, 0, 4), true); err != nil || !v.Repeat.Exhausted || v.State != "completed" {
		t.Fatal(v, err)
	}
}

func TestRecurrenceKeepsTodayAndOriginalAnchor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 31, 15, 0, 0, 0, time.UTC)
	v := Todo{State: "open", Repeat: &Repeat{Rule: "FREQ=MONTHLY", Next: "2026-01-31", Zone: "UTC"}}
	if changed, err := advanceTodo(&v, now, false); err != nil || changed {
		t.Fatal("today was skipped", v, err)
	}
	if changed, err := advanceTodo(&v, now.AddDate(0, 1, 0), false); err != nil || !changed || v.Repeat.Next != "2026-03-31" || v.Repeat.Start != "2026-01-31" {
		t.Fatal(v, err)
	}
	if changed, err := advanceTodo(&v, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), false); err != nil || !changed || v.Repeat.Next != "2026-05-31" || v.Repeat.Start != "2026-01-31" {
		t.Fatal("month anchor drifted", v, err)
	}
}

func TestCompletionRelativeTimestampKeepsPrecision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 10, 12, 34, 56, 123456789, time.UTC)
	v := Todo{State: "completed", Repeat: &Repeat{Rule: "FREQ=DAILY;INTERVAL=3", Next: "2026-01-01T09:00:31.12Z", FromCompletion: true}}
	if _, err := advanceTodo(&v, now, true); err != nil || v.Repeat.Next != "2026-01-13T12:34:56.123456789Z" {
		t.Fatal(v.Repeat, err)
	}
}
