// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
)

// The set is the fixed input of a measurement, so a typo in it changes a
// published number. These checks hold the set's shape and the scoring rules
// that read it, not the answers a model gives.
func TestSetLoads(t *testing.T) {
	set, err := loadSet("../../testdata/localmodel-eval/set.json")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, c := range set.Cases {
		counts[c.Kind]++
		if c.input() == "" {
			t.Fatalf("%s: empty input", c.ID)
		}
		if len(c.Schema) > 0 && (len(c.Expect.Fields)+len(c.Expect.FieldAny) == 0) {
			t.Fatalf("%s: structured case checks no field", c.ID)
		}
		if len(c.Schema) == 0 && len(c.Expect.MustInclude)+len(c.Expect.AnyOf) == 0 {
			t.Fatalf("%s: text case checks no content", c.ID)
		}
	}
	for _, kind := range []string{"intent", "autocomplete", "rewrite", "title", "classify", "extract", "summary"} {
		if counts[kind] < 3 {
			t.Fatalf("%s has %d cases; the table needs at least 3", kind, counts[kind])
		}
	}
}

func TestNormalizeDate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2026-10-25", "2026-10-25"},
		{"2026년 10월 25일", "2026-10-25"},
		{"2026.11.3", "2026-11-03"},
		{"2026/12/01", "2026-12-01"},
		{"2026-11-03 09:30", "2026-11-03"},
		{"10월 25일", "10월 25일"},
	} {
		if got := normalizeDate(tc.in); got != tc.want {
			t.Errorf("normalizeDate(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTime(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"15:00", "15:00"},
		{"오후 3시", "15:00"},
		{"오전 10시", "10:00"},
		{"3:00 PM", "15:00"},
		{"09:30", "09:30"},
		{"12:00 오전", "00:00"},
	} {
		if got := normalizeTime(tc.in); got != tc.want {
			t.Errorf("normalizeTime(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestPercentile(t *testing.T) {
	values := []float64{5, 1, 4, 2, 3, 6, 7, 8, 9, 10}
	if got := percentile(values, 0.50); got != 5 {
		t.Errorf("p50=%v want 5", got)
	}
	if got := percentile(values, 0.95); got != 10 {
		t.Errorf("p95=%v want 10", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("empty=%v want 0", got)
	}
	if values[0] != 5 {
		t.Errorf("percentile reordered its input")
	}
}
