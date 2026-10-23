// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"github.com/thisisjun786/lina/kit/calendar"
	"time"
)

// The shared calculator is pure; durable timer and record stores stay separate.
type Schedule = calendar.Schedule

var ErrInvalid = calendar.ErrInvalid

func Recurrence(rule string, start time.Time, zone string) (Schedule, error) {
	return calendar.Recurrence(rule, start, zone)
}
func Next(rule string, from time.Time, zone string) (time.Time, error) {
	return calendar.Next(rule, from, zone)
}
