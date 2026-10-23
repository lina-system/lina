// SPDX-License-Identifier: Apache-2.0
package calendar

import (
	"errors"
	"testing"
	"time"
)

func TestScheduleAt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{"at is after the cursor", at.Add(-time.Minute), at},
		{"at is before the cursor", at.Add(time.Minute), time.Time{}},
		{"at equals the cursor", at, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := (Schedule{At: at}).Next(tc.after)
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

func TestScheduleNextRejectsInvalid(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC)
	start := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		s    Schedule
	}{
		{"at with cron", Schedule{At: at, Cron: "0 9 * * *", Zone: "UTC"}},
		{"at with rrule", Schedule{At: at, RRule: "FREQ=DAILY", Start: start, Zone: "UTC"}},
		{"missing zone", Schedule{Cron: "0 9 * * *"}},
		{"unknown zone", Schedule{Cron: "0 9 * * *", Zone: "Not/AZone"}},
		{"both cron and rrule", Schedule{Cron: "0 9 * * *", RRule: "FREQ=DAILY", Start: start, Zone: "UTC"}},
		{"neither cron nor rrule", Schedule{Zone: "UTC"}},
		{"cron with four fields", Schedule{Cron: "0 9 * *", Zone: "UTC"}},
		{"cron with six fields", Schedule{Cron: "0 9 * * * *", Zone: "UTC"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.s.Next(start)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %s err %v, want ErrInvalid", got.Format(time.RFC3339), err)
			}
		})
	}
}

func TestScheduleNextInstant(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		s           Schedule
		after, want time.Time
	}{
		{
			name:  "cron",
			s:     Schedule{Zone: "UTC", Cron: "30 9 * * *"},
			after: time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC),
			want:  time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC),
		},
		{
			name:  "cron dst gap",
			s:     Schedule{Zone: "America/New_York", Cron: "30 2 * * *"},
			after: time.Date(2026, 3, 8, 6, 59, 59, 0, time.UTC),
			want:  time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC),
		},
		{
			name: "rrule",
			s: Schedule{
				Zone:  "UTC",
				RRule: "FREQ=DAILY",
				Start: time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC),
			},
			after: time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC),
			want:  time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC),
		},
		{
			name: "rrule dst fold",
			s: Schedule{
				Zone:  "America/New_York",
				RRule: "FREQ=DAILY;BYHOUR=1;BYMINUTE=30",
				Start: time.Date(2026, 10, 31, 5, 30, 0, 0, time.UTC),
			},
			after: time.Date(2026, 11, 1, 4, 59, 59, 0, time.UTC),
			want:  time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.s.Next(tc.after)
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
