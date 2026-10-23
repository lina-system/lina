// SPDX-License-Identifier: Apache-2.0
package records

import "time"

// Calendar bounds come from the caller's explicit civil day and IANA zone.
// AddDate, rather than 24 hours, keeps DST transition days correct.
func viewBounds(p Params) ([]any, error) {
	switch p.View {
	case "", "memos", "board", "list", "anytime", "later":
	case "today", "upcoming", "week":
		if p.Day == "" || p.Zone == "" {
			return nil, ErrInvalid
		}
	default:
		return nil, ErrInvalid
	}
	day := p.Day
	if day == "" {
		day = "2000-01-01"
	}
	zone := p.Zone
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, ErrInvalid
	}
	d, err := time.ParseInLocation(time.DateOnly, day, loc)
	if err != nil || d.Format(time.DateOnly) != day {
		return nil, ErrAmbiguousDate
	}
	// The week lens is the seven calendar days starting at Day.
	return []any{day, d.UTC().Format(time.RFC3339), d.AddDate(0, 0, 1).UTC().Format(time.RFC3339), d.AddDate(0, 0, 7).UTC().Format(time.RFC3339), d.AddDate(0, 0, 7).Format(time.DateOnly)}, nil
}

func dateBefore(column, day, instant string) string {
	return "(" + column + "<>'' AND ((length(" + column + ")=10 AND " + column + "<" + day + ") OR (length(" + column + ")>10 AND julianday(" + column + ")<julianday(" + instant + "))))"
}
func dateOnDay(column string) string {
	return "(" + column + "<>'' AND ((length(" + column + ")=10 AND " + column + "=b.day) OR (length(" + column + ")>10 AND julianday(" + column + ")>=julianday(b.start) AND julianday(" + column + ")<julianday(b.end))))"
}
func viewFilter(view string) string {
	active := "t.kind='todo' AND t.state NOT IN ('completed','canceled')"
	today := "(" + dateBefore("v.when_value", "date(b.day,'+1 day')", "b.end") + " OR " + dateOnDay("v.deadline") + " OR " + dateOnDay("v.repeat_next") + ")"
	switch view {
	case "memos":
		return "t.kind='memo' AND t.state NOT IN ('archived','canceled')"
	case "today":
		return active + " AND " + today
	case "upcoming":
		future := func(c string) string {
			return "(" + c + "<>'' AND NOT " + dateBefore(c, "date(b.day,'+1 day')", "b.end") + ")"
		}
		return active + " AND (" + future("v.when_value") + " OR " + future("v.deadline") + " OR " + future("v.repeat_next") + ")"
	case "week":
		within := func(c string) string {
			return "(" + dateBefore(c, "b.week_day", "b.week_end") + " AND NOT " + dateBefore(c, "b.day", "b.start") + ")"
		}
		return active + " AND (" + within("v.when_value") + " OR " + within("v.deadline") + " OR " + within("v.repeat_next") + ")"
	case "anytime":
		return active + " AND v.when_value='' AND v.deadline='' AND v.repeat_next=''"
	case "later":
		return "t.kind='todo' AND t.state='backlog'"
	case "board", "list":
		return "t.kind='todo'"
	default:
		return "1=1"
	}
}
