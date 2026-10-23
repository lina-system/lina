// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/timers"
)

func repeatTime(value, zone string) (time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	if len(value) == 10 {
		return time.ParseInLocation(time.DateOnly, value, loc)
	}
	return time.Parse(time.RFC3339, value)
}
func repeatSchedule(r Repeat) (timers.Schedule, time.Time, error) {
	zone := r.Zone
	if zone == "" {
		zone = "UTC"
	}
	current, err := repeatTime(r.Next, zone)
	if err != nil {
		return timers.Schedule{}, current, ErrAmbiguousDate
	}
	start := current
	if r.Start != "" {
		start, err = repeatTime(r.Start, zone)
	}
	if err != nil || r.Completed < 0 {
		return timers.Schedule{}, current, ErrInvalid
	}
	s, err := timers.Recurrence(r.Rule, start, zone)
	if err != nil {
		return s, current, ErrInvalid
	}
	return s, current, nil
}

// advanceTodo keeps one live record and retains the original recurrence anchor.
// A date-only occurrence is overdue after its civil day, never at midnight on
// that day. Exhaustion ends scheduling without inventing completion evidence.
func advanceTodo(v *Todo, now time.Time, completed bool) (bool, error) {
	if v.Repeat == nil || v.Repeat.Exhausted {
		return false, nil
	}
	r := *v.Repeat
	s, current, err := repeatSchedule(r)
	if err != nil {
		return false, err
	}
	cutoff := current
	if len(r.Next) == 10 {
		loc, _ := time.LoadLocation(s.Zone)
		cutoff = current.In(loc).AddDate(0, 0, 1)
	}
	if !completed && now.Before(cutoff) {
		return false, nil
	}
	if r.Start == "" {
		r.Start = r.Next
	}
	if completed {
		r.Completed++
	}
	after := now
	if after.Before(current) {
		after = current
	}
	if r.FromCompletion {
		// COUNT in a completion-relative series counts completed occurrences, not
		// missed calendar slots. Keep UNTIL and the person's original rule intact.
		parts := []string{}
		for _, part := range strings.Split(strings.TrimPrefix(r.Rule, "RRULE:"), ";") {
			if n, ok := strings.CutPrefix(part, "COUNT="); ok {
				count, _ := strconv.Atoi(n)
				if r.Completed >= count {
					r.Exhausted = true
				}
			} else {
				parts = append(parts, part)
			}
		}
		s.RRule = strings.Join(parts, ";")
		if completed {
			after = now
			s.Start = now.Truncate(time.Minute)
			if len(r.Next) == 10 {
				loc, _ := time.LoadLocation(s.Zone)
				t := now.In(loc)
				s.Start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
			}
			loc, _ := time.LoadLocation(s.Zone)
			if len(r.Next) == 10 {
				r.Start = s.Start.In(loc).Format(time.DateOnly)
			} else {
				r.Start = s.Start.In(loc).Format(time.RFC3339)
			}
		}
	}
	var next time.Time
	if !r.Exhausted {
		next, err = s.Next(after)
		if err != nil {
			return false, ErrInvalid
		}
	}
	if next.IsZero() {
		r.Exhausted = true
	} else {
		loc, _ := time.LoadLocation(s.Zone)
		if len(r.Next) == 10 {
			r.Next = next.In(loc).Format(time.DateOnly)
		} else {
			r.Next = next.In(loc).Format(time.RFC3339)
		}
		v.State = "open"
		v.When = ""
		v.BlockedBy = nil
		v.WaitingOn = nil
		for i := range v.Checklist {
			v.Checklist[i].Checked = false
		}
	}
	v.Repeat = &r
	return true, nil
}

func transformCompletion(data []byte, old document, now time.Time) ([]byte, error) {
	next, err := parseDocument(data)
	if err != nil {
		return nil, err
	}
	if old.todo == nil || old.state == "completed" || next.todo == nil || next.state != "completed" || next.todo.Repeat == nil {
		return data, nil
	}
	if _, err = advanceTodo(next.todo, now, true); err != nil {
		return nil, err
	}
	front, body, _ := strings.Cut(string(data), "\n---\n")
	front, err = WithTodo(front, *next.todo)
	return []byte(front + "\n---\n" + body), err
}

// AdvanceRecurring is a model-free maintenance pass. Commit checks each saved
// revision again, so a concurrent edit wins without being overwritten.
func (s *Store) AdvanceRecurring(ctx context.Context, a Actor, now time.Time) error {
	after := ""
	for {
		rows, err := s.Query(ctx, Params{Person: a.Person, Kind: "todo", After: after, Limit: 100})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Todo == nil || row.State == "completed" || row.State == "canceled" {
				continue
			}
			changed, err := advanceTodo(row.Todo, now, false)
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			rec, rev, err := s.Read(ctx, row.ID)
			if err != nil {
				return err
			}
			if rev != row.Revision {
				continue
			}
			front, body, _ := strings.Cut(string(rec.Data), "\n---\n")
			front, err = WithTodo(front, *row.Todo)
			if err != nil {
				return err
			}
			_, err = s.Commit(ctx, Change{ID: row.ID, Base: rev, Path: rec.Path, Data: []byte(front + "\n---\n" + body)}, a)
			if err != nil && !errors.Is(err, ErrStaleRevision) {
				return err
			}
		}
		if len(rows) < 100 {
			return nil
		}
		after = rows[len(rows)-1].ID
	}
}

// RepeatFromFront is used by the tool boundary to preserve Core's counters.
func RepeatFromFront(front string) *Repeat {
	for _, line := range strings.Split(front, "\n") {
		if raw, ok := strings.CutPrefix(line, "repeat: "); ok {
			var r Repeat
			if json.Unmarshal([]byte(raw), &r) == nil {
				return &r
			}
		}
	}
	return nil
}
