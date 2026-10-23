// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

var ErrInvalid = errors.New("invalid timer")

// Schedule has exactly one of At, Cron or RRule. Recurrences have minute
// precision and use an explicit IANA zone; Start anchors RRULE intervals.
type Schedule struct {
	At    time.Time `json:"at,omitempty"`
	Start time.Time `json:"start,omitempty"`
	Zone  string    `json:"zone,omitempty"`
	Cron  string    `json:"cron,omitempty"`
	RRule string    `json:"rrule,omitempty"`
}

type calendar struct {
	loc                                    *time.Location
	hours, minutes, months, days, weekdays []int
	cron                                   bool
	anyDay, anyWeek                        bool
	freq                                   string
	interval                               int
	start, until                           time.Time
	count                                  int
}

// Next returns the first distinct instant after after, or zero when exhausted.
// Civil candidates are resolved to the earlier occurrence in a fold, and to
// the transition instant in a gap. Several gap candidates share one instant.
func (s Schedule) Next(after time.Time) (time.Time, error) {
	if !s.At.IsZero() {
		if s.Cron != "" || s.RRule != "" {
			return time.Time{}, ErrInvalid
		}
		if s.At.After(after) {
			return s.At.UTC(), nil
		}
		return time.Time{}, nil
	}
	c, err := s.parse()
	if err != nil {
		return time.Time{}, err
	}
	from := after.In(c.loc)
	if c.count > 0 || from.Before(c.start) {
		from = c.start
	}
	day := civilDate(from)
	// The Gregorian calendar repeats every 400 years. A bounded search also
	// rejects impossible schedules without an unbounded scheduler transaction.
	end := day.AddDate(400, 0, 0)
	seen := 0
	var previous time.Time
	for ; day.Before(end); day = day.AddDate(0, 0, 1) {
		if !c.dateMatches(day) {
			continue
		}
		instants := []time.Time{}
		offsets, gaps := zoneDay(day, c.loc)
		for _, h := range c.hours {
			for _, m := range c.minutes {
				civil := day.Add(time.Duration(h*60+m) * time.Minute)
				if !c.timeMatches(civil) {
					continue
				}
				at := resolveCivil(civil, c.loc, offsets, gaps)
				if at.IsZero() || (!c.start.IsZero() && at.Before(c.start)) {
					continue
				}
				instants = append(instants, at)
			}
		}
		sort.Slice(instants, func(i, j int) bool { return instants[i].Before(instants[j]) })
		for _, at := range instants {
			if at.Equal(previous) {
				continue
			}
			previous = at
			if !c.until.IsZero() && at.After(c.until) {
				return time.Time{}, nil
			}
			seen++
			if c.count > 0 && seen > c.count {
				return time.Time{}, nil
			}
			if at.After(after) {
				return at, nil
			}
			if c.count > 0 && seen == c.count {
				return time.Time{}, nil
			}
		}
		if !c.until.IsZero() && day.After(c.until.Add(48*time.Hour)) {
			return time.Time{}, nil
		}
	}
	return time.Time{}, ErrInvalid
}

func (s Schedule) parse() (calendar, error) {
	c := calendar{interval: 1, cron: s.Cron != ""}
	if s.Zone == "" || (s.Cron == "") == (s.RRule == "") {
		return c, ErrInvalid
	}
	var err error
	c.loc, err = time.LoadLocation(s.Zone)
	if err != nil {
		return c, ErrInvalid
	}
	c.hours = sequence(0, 23)
	c.minutes = sequence(0, 59)
	c.months = sequence(1, 12)
	c.days = sequence(1, 31)
	c.weekdays = sequence(0, 6)
	c.anyDay = true
	c.anyWeek = true
	if c.cron {
		parts := strings.Fields(s.Cron)
		if len(parts) != 5 {
			return c, ErrInvalid
		}
		targets := []*[]int{&c.minutes, &c.hours, &c.days, &c.months, &c.weekdays}
		lows := []int{0, 0, 1, 1, 0}
		highs := []int{59, 23, 31, 12, 7}
		for i, p := range parts {
			*targets[i], err = numbers(p, lows[i], highs[i], true)
			if err != nil {
				return c, err
			}
		}
		for i, v := range c.weekdays {
			if v == 7 {
				c.weekdays[i] = 0
			}
		}
		c.anyDay = strings.HasPrefix(parts[2], "*")
		c.anyWeek = strings.HasPrefix(parts[4], "*")
		return c, nil
	}
	if s.Start.IsZero() || s.Start.Second() != 0 || s.Start.Nanosecond() != 0 {
		return c, ErrInvalid
	}
	c.start = s.Start.In(c.loc)
	values := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(s.RRule, "RRULE:"), ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || v == "" || values[k] != "" {
			return c, ErrInvalid
		}
		values[k] = v
	}
	c.freq = values["FREQ"]
	switch c.freq {
	case "MINUTELY", "HOURLY", "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		return c, ErrInvalid
	}
	if c.freq != "MINUTELY" {
		c.minutes = []int{c.start.Minute()}
	}
	if c.freq != "MINUTELY" && c.freq != "HOURLY" {
		c.hours = []int{c.start.Hour()}
	}
	if c.freq == "MONTHLY" || c.freq == "YEARLY" {
		c.days = []int{c.start.Day()}
	}
	if c.freq == "YEARLY" && values["BYMONTHDAY"] == "" && values["BYDAY"] == "" {
		c.months = []int{int(c.start.Month())}
	}
	if c.freq == "WEEKLY" {
		c.weekdays = []int{int(c.start.Weekday())}
	}
	for k, v := range values {
		switch k {
		case "FREQ":
		case "INTERVAL", "COUNT":
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 100000 {
				return c, ErrInvalid
			}
			if k == "INTERVAL" {
				c.interval = n
			} else {
				c.count = n
			}
		case "UNTIL":
			c.until, err = time.Parse("20060102T150405Z", v)
			if err != nil {
				c.until, err = time.Parse(time.RFC3339, v)
			}
			if err != nil {
				return c, ErrInvalid
			}
		case "BYMINUTE":
			c.minutes, err = numbers(v, 0, 59, false)
		case "BYHOUR":
			c.hours, err = numbers(v, 0, 23, false)
		case "BYMONTH":
			c.months, err = numbers(v, 1, 12, false)
		case "BYMONTHDAY":
			c.days, err = numbers(v, -31, 31, false)
			if contains(c.days, 0) {
				return c, ErrInvalid
			}
		case "BYDAY":
			c.weekdays = nil
			for _, d := range strings.Split(v, ",") {
				i := strings.Index("SUMOTUWETHFRSA", d)
				if len(d) != 2 || i < 0 || i%2 != 0 {
					return c, ErrInvalid
				}
				c.weekdays = append(c.weekdays, i/2)
			}
			if values["BYMONTHDAY"] == "" && (c.freq == "MONTHLY" || c.freq == "YEARLY") {
				c.days = sequence(1, 31)
			}
		default:
			return c, ErrInvalid
		}
		if err != nil {
			return c, err
		}
	}
	if c.count > 0 && !c.until.IsZero() {
		return c, ErrInvalid
	}
	// Next's civil-day search excludes the day 400 years ahead. Refuse
	// intervals that cannot advance inside it even when Start itself matches.
	// Shorter frequencies already fit under the parser's 100000 ceiling.
	switch c.freq {
	case "YEARLY":
		if c.interval >= 400 {
			return c, ErrInvalid
		}
	case "MONTHLY":
		if c.interval >= 400*12 {
			return c, ErrInvalid
		}
	case "WEEKLY":
		if c.interval >= 146097/7 { // Gregorian days in 400 years.
			return c, ErrInvalid
		}
	}
	return c, nil
}

func sequence(lo, hi int) []int {
	out := []int{}
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}
func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
func numbers(s string, lo, hi int, cron bool) ([]int, error) {
	out := []int{}
	for _, part := range strings.Split(s, ",") {
		base, stepText, stepped := strings.Cut(part, "/")
		step := 1
		if stepped {
			var e error
			step, e = strconv.Atoi(stepText)
			if !cron || e != nil || step < 1 || step > hi-lo+1 {
				return nil, ErrInvalid
			}
		}
		a, b := lo, hi
		if base != "*" {
			left, right, ranged := strings.Cut(base, "-")
			if strings.HasPrefix(base, "-") && !cron {
				left = base
				ranged = false
			}
			var e error
			a, e = strconv.Atoi(left)
			if e != nil {
				return nil, ErrInvalid
			}
			b = a
			if ranged {
				if !cron {
					return nil, ErrInvalid
				}
				b, e = strconv.Atoi(right)
				if e != nil {
					return nil, ErrInvalid
				}
			} else if stepped {
				b = hi
			}
		} else if !cron {
			return nil, ErrInvalid
		}
		if a < lo || b > hi || a > b {
			return nil, ErrInvalid
		}
		for v := a; v <= b; v += step {
			if !contains(out, v) {
				out = append(out, v)
			}
		}
	}
	sort.Ints(out)
	return out, nil
}
func civilDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
func civilTime(t time.Time) time.Time {
	return civilDate(t).Add(time.Duration(t.Hour()*60+t.Minute()) * time.Minute)
}
func (c calendar) dateMatches(d time.Time) bool {
	if !contains(c.months, int(d.Month())) {
		return false
	}
	last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	dayMatch := contains(c.days, d.Day()) || contains(c.days, d.Day()-last-1)
	weekMatch := contains(c.weekdays, int(d.Weekday()))
	if c.cron {
		if !c.anyDay && !c.anyWeek {
			return dayMatch || weekMatch
		}
		return dayMatch && weekMatch
	}
	if !dayMatch || !weekMatch {
		return false
	}
	start := civilDate(c.start)
	diff := int((d.Unix() - start.Unix()) / 86400)
	switch c.freq {
	case "DAILY":
		return diff >= 0 && diff%c.interval == 0
	case "WEEKLY":
		monday := start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
		weeks := int((d.Unix() - monday.Unix()) / (7 * 86400))
		return weeks >= 0 && weeks%c.interval == 0
	case "MONTHLY":
		n := (d.Year()-start.Year())*12 + int(d.Month()-start.Month())
		return n >= 0 && n%c.interval == 0
	case "YEARLY":
		n := d.Year() - start.Year()
		return n >= 0 && n%c.interval == 0
	default:
		return true
	}
}
func (c calendar) timeMatches(t time.Time) bool {
	if c.cron {
		return true
	}
	start := civilTime(c.start)
	switch c.freq {
	case "MINUTELY":
		n := int((t.Unix() - start.Unix()) / 60)
		return n >= 0 && n%c.interval == 0
	case "HOURLY":
		n := int((t.Truncate(time.Hour).Unix() - start.Truncate(time.Hour).Unix()) / 3600)
		return n >= 0 && n%c.interval == 0
	default:
		return true
	}
}

type gap struct{ from, to, at time.Time }

// Traverse actual zone boundaries (including non-hour and skipped-day changes).
func zoneDay(day time.Time, loc *time.Location) ([]int, []gap) {
	offsets := []int{}
	gaps := []gap{}
	end := day.Add(72 * time.Hour)
	for at := day.Add(-48 * time.Hour); at.Before(end); {
		local := at.In(loc)
		_, off := local.Zone()
		if !contains(offsets, off) {
			offsets = append(offsets, off)
		}
		_, next := local.ZoneBounds()
		if next.IsZero() || !next.Before(end) {
			break
		}
		_, newOff := next.In(loc).Zone()
		if newOff > off {
			gaps = append(gaps, gap{next.UTC().Add(time.Duration(off) * time.Second), next.UTC().Add(time.Duration(newOff) * time.Second), next.UTC()})
		}
		at = next
	}
	return offsets, gaps
}
func resolveCivil(civil time.Time, loc *time.Location, offsets []int, gaps []gap) time.Time {
	var best time.Time
	for _, off := range offsets {
		at := civil.Add(-time.Duration(off) * time.Second)
		if civilTime(at.In(loc)).Equal(civil) && (best.IsZero() || at.Before(best)) {
			best = at
		}
	}
	if !best.IsZero() {
		return best
	}
	for _, g := range gaps {
		if !civil.Before(g.from) && civil.Before(g.to) {
			return g.at
		}
	}
	return time.Time{}
}
