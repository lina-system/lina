// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"testing"
	"time"
)

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCivilTransitions(t *testing.T) {
	for _, tc := range []struct{ name, zone, cron, rule, start, after, want, following string }{
		{"gap", "America/New_York", "30 2 * * *", "FREQ=DAILY;BYHOUR=2;BYMINUTE=30", "2026-03-07T07:30:00Z", "2026-03-08T06:59:59Z", "2026-03-08T07:00:00Z", "2026-03-09T06:30:00Z"},
		{"fold", "America/New_York", "30 1 * * *", "FREQ=DAILY;BYHOUR=1;BYMINUTE=30", "2026-10-31T05:30:00Z", "2026-11-01T04:59:59Z", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
		{"half-hour gap", "Australia/Lord_Howe", "15 2 * * *", "FREQ=DAILY;BYHOUR=2;BYMINUTE=15", "2026-10-02T15:45:00Z", "2026-10-03T15:00:00Z", "2026-10-03T15:30:00Z", "2026-10-04T15:15:00Z"},
	} {
		for _, form := range []string{"cron", "rrule"} {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				s := Schedule{Zone: tc.zone, Start: instant(tc.start)}
				if form == "cron" {
					s.Cron = tc.cron
				} else {
					s.RRule = tc.rule
				}
				got, err := s.Next(instant(tc.after))
				if err != nil || !got.Equal(instant(tc.want)) {
					t.Fatalf("next=%s err=%v want=%s", got, err, tc.want)
				}
				got, err = s.Next(got)
				if err != nil || !got.Equal(instant(tc.following)) {
					t.Fatalf("following=%s err=%v want=%s", got, err, tc.following)
				}
			})
		}
	}
}

func TestRecurrenceForms(t *testing.T) {
	for _, tc := range []struct{ rule, start, after, want string }{
		{"FREQ=DAILY;COUNT=2", "2026-01-01T09:30:00Z", "2026-01-01T09:30:00Z", "2026-01-02T09:30:00Z"},
		{"FREQ=DAILY;COUNT=2", "2026-01-01T09:30:00Z", "2026-01-02T09:30:00Z", ""},
		{"FREQ=DAILY;UNTIL=20260102T093000Z", "2026-01-01T09:30:00Z", "2026-01-02T09:30:00Z", ""},
		{"FREQ=MINUTELY;INTERVAL=15", "2026-01-01T09:30:00Z", "2026-01-01T09:31:00Z", "2026-01-01T09:45:00Z"},
		{"FREQ=HOURLY;INTERVAL=2;BYMINUTE=0,30", "2026-01-01T09:00:00Z", "2026-01-01T09:31:00Z", "2026-01-01T11:00:00Z"},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE", "2026-01-05T09:00:00Z", "2026-01-07T09:00:00Z", "2026-01-19T09:00:00Z"},
		{"FREQ=MONTHLY", "2026-01-31T09:00:00Z", "2026-01-31T09:00:00Z", "2026-03-31T09:00:00Z"},
		{"FREQ=MONTHLY;BYMONTHDAY=-1", "2026-01-31T09:00:00Z", "2026-01-31T09:00:00Z", "2026-02-28T09:00:00Z"},
		{"FREQ=YEARLY;BYDAY=MO", "2026-01-01T09:00:00Z", "2026-01-31T09:00:00Z", "2026-02-02T09:00:00Z"},
		{"FREQ=YEARLY;BYMONTHDAY=1", "2026-01-01T09:00:00Z", "2026-01-01T09:00:00Z", "2026-02-01T09:00:00Z"},
		{"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29", "2024-02-29T09:00:00Z", "2024-02-29T09:00:00Z", "2028-02-29T09:00:00Z"},
	} {
		t.Run(tc.rule+tc.after, func(t *testing.T) {
			s := Schedule{Start: instant(tc.start), Zone: "UTC", RRule: tc.rule}
			next, err := s.Next(instant(tc.after))
			var want time.Time
			if tc.want != "" {
				want = instant(tc.want)
			}
			if err != nil || !next.Equal(want) {
				t.Fatalf("next=%s want=%s err=%v", next, want, err)
			}
		})
	}
	for _, s := range []Schedule{
		{}, {Cron: "0 0 * * *", Zone: "unknown"}, {Cron: "* * * * * *", Zone: "UTC"}, {Cron: "*/0 * * * *", Zone: "UTC"}, {At: instant("2026-01-01T00:00:00Z"), Cron: "* * * * *", Zone: "UTC"},
		{Start: instant("2026-01-01T00:00:00Z"), Zone: "UTC", RRule: "FREQ=DAILY;UNKNOWN=1"},
		{Start: instant("2026-01-01T00:00:00Z"), Zone: "UTC", RRule: "FREQ=DAILY;COUNT=1;UNTIL=20260102T000000Z"},
	} {
		if _, err := s.Next(instant("2026-01-01T00:00:00Z")); err == nil {
			t.Fatalf("invalid schedule accepted: %+v", s)
		}
	}
}

func TestRecurrenceIntervalHorizonAdmission(t *testing.T) {
	for _, rule := range []string{
		"FREQ=YEARLY;INTERVAL=400", "FREQ=YEARLY;INTERVAL=500",
		"FREQ=MONTHLY;INTERVAL=4800", "FREQ=MONTHLY;INTERVAL=6000",
		"FREQ=WEEKLY;INTERVAL=20871", "FREQ=WEEKLY;INTERVAL=100000",
	} {
		t.Run(rule, func(t *testing.T) {
			s, _ := openTimers(t, t.TempDir(), 1)
			now := instant("2026-01-01T00:00:00Z")
			tm := timerFixture("oversize", "reminder")
			tm.Schedule = Schedule{Start: now.Add(time.Hour), Zone: "UTC", RRule: rule}
			if _, err := s.Create(t.Context(), tm, now); err == nil {
				t.Fatal("accepted interval outside advancement horizon")
			}
			normal := timerFixture("normal", "reminder")
			if _, err := s.Create(t.Context(), normal, now); err != nil {
				t.Fatal(err)
			}
			fires, err := s.ClaimDue(t.Context(), now.Add(time.Hour), "1")
			if err != nil || len(fires) != 1 || fires[0].Timer.ID != normal.ID {
				t.Fatalf("unrelated timer blocked: fires=%+v err=%v", fires, err)
			}
		})
	}
}

func TestRecurrenceIntervalsWithinHorizon(t *testing.T) {
	for _, rule := range []string{
		"FREQ=YEARLY;INTERVAL=399", "FREQ=MONTHLY;INTERVAL=4799",
		"FREQ=WEEKLY;INTERVAL=20870", "FREQ=DAILY;INTERVAL=100000",
		"FREQ=HOURLY;INTERVAL=100000", "FREQ=MINUTELY;INTERVAL=100000",
	} {
		t.Run(rule, func(t *testing.T) {
			start := instant("2026-01-01T00:00:00Z")
			s := Schedule{Start: start, Zone: "UTC", RRule: rule}
			first, err := s.Next(start.Add(-time.Minute))
			if err != nil || !first.Equal(start) {
				t.Fatal(first, err)
			}
			next, err := s.Next(first)
			if err != nil || !next.After(first) || !next.Before(start.AddDate(400, 0, 0)) {
				t.Fatal(next, err)
			}
		})
	}
}
