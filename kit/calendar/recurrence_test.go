// SPDX-License-Identifier: Apache-2.0
package calendar

import (
	"errors"
	"testing"
	"time"
)

func TestRecurrenceRules(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		rule    string
		wantErr bool
	}{
		{"unknown freq", "FREQ=MINUTELY", true},
		{"unknown key", "FREQ=DAILY;BYHOUR=9", true},
		{"interval", "FREQ=DAILY;INTERVAL=3", false},
		{"byday", "FREQ=WEEKLY;BYDAY=MO,WE", false},
		{"bymonthday", "FREQ=MONTHLY;BYMONTHDAY=15", false},
		{"count", "FREQ=DAILY;COUNT=4", false},
		{"until", "FREQ=DAILY;UNTIL=20260110T090000Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Recurrence(tc.rule, start, "UTC")
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("got %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Recurrence: %v", err)
			}
		})
	}
}

func TestNextReturnsStrictlyFuture(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		rule, zone string
		from, want time.Time
	}{
		{
			name: "daily",
			rule: "FREQ=DAILY",
			zone: "UTC",
			from: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "weekly byday",
			rule: "FREQ=WEEKLY;BYDAY=MO,WE",
			zone: "UTC",
			from: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 1, 7, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "monthly bymonthday",
			rule: "FREQ=MONTHLY;BYMONTHDAY=-1",
			zone: "UTC",
			from: time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Next(tc.rule, tc.from, tc.zone)
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !got.After(tc.from) {
				t.Fatalf("got %s, not after %s", got.Format(time.RFC3339), tc.from.Format(time.RFC3339))
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
