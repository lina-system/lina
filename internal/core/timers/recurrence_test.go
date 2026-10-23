// SPDX-License-Identifier: Apache-2.0
package timers

import "testing"

func TestTodoRecurrenceNextCorpus(t *testing.T) {
	for _, tc := range []struct{ rule, from, zone, want string }{
		{"FREQ=DAILY;INTERVAL=3", "2026-01-01T09:00:00Z", "UTC", "2026-01-04T09:00:00Z"},
		{"FREQ=DAILY", "2026-01-01T09:00:31.123456789Z", "UTC", "2026-01-02T09:00:31.123456789Z"},
		{"FREQ=WEEKLY;BYDAY=MO,WE", "2026-01-05T09:00:00Z", "UTC", "2026-01-07T09:00:00Z"},
		{"FREQ=MONTHLY", "2026-01-31T09:00:00Z", "UTC", "2026-03-31T09:00:00Z"},
		{"FREQ=MONTHLY;BYMONTHDAY=-1", "2026-01-31T09:00:00Z", "UTC", "2026-02-28T09:00:00Z"},
		{"FREQ=YEARLY", "2024-02-29T09:00:00Z", "UTC", "2028-02-29T09:00:00Z"},
		{"FREQ=DAILY", "2026-03-07T07:30:00Z", "America/New_York", "2026-03-08T07:00:00Z"},
		{"FREQ=DAILY", "2026-10-31T05:30:00Z", "America/New_York", "2026-11-01T05:30:00Z"},
	} {
		got, err := Next(tc.rule, instant(tc.from), tc.zone)
		if err != nil || !got.Equal(instant(tc.want)) || !got.After(instant(tc.from)) {
			t.Errorf("%+v: got %s err %v", tc, got, err)
		}
	}
	for _, rule := range []string{"FREQ=DAILY;COUNT=1", "FREQ=DAILY;UNTIL=20260101T090000Z"} {
		got, err := Next(rule, instant("2026-01-01T09:00:00Z"), "UTC")
		if err != nil || !got.IsZero() {
			t.Fatal("exhaustion", got, err)
		}
	}
}
