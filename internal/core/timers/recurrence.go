// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"strings"
	"time"
)

// Recurrence is the to-do subset of the native calendar calculator. Start is
// retained across advances so month ends, intervals and COUNT cannot drift.
func Recurrence(rule string, start time.Time, zone string) (Schedule, error) {
	s := Schedule{RRule: rule, Start: start, Zone: zone}
	for _, part := range strings.Split(strings.TrimPrefix(rule, "RRULE:"), ";") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "FREQ":
			switch value {
			case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
			default:
				return s, ErrInvalid
			}
		case "INTERVAL", "BYDAY", "BYMONTHDAY", "COUNT", "UNTIL":
		default:
			return s, ErrInvalid
		}
	}
	_, err := s.parse()
	return s, err
}

// Next returns a strictly future occurrence anchored at from. Use Recurrence
// with the original Start when advancing an existing series.
func Next(rule string, from time.Time, zone string) (time.Time, error) {
	s, err := Recurrence(rule, from, zone)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(from)
}
