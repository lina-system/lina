// SPDX-License-Identifier: Apache-2.0
package mocks

import (
	"github.com/thisisjun786/lina/kit/result"
	"testing"
)

func TestStopRules(t *testing.T) {
	checks := func(n int) []result.MockCheck {
		out := []result.MockCheck{}
		for i, id := range Dimensions {
			out = append(out, result.MockCheck{ID: id, Passed: i < n})
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		scores []int
		stop   string
	}{
		{"first-pass", []int{7}, "all_passed"},
		{"early-pass", []int{4, 7}, "all_passed"},
		{"plateau", []int{4, 4}, "no_improvement"},
		{"regression", []int{4, 3}, "no_improvement"},
		{"three-redraws", []int{1, 2, 3, 4}, "redraw_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts []Attempt
			for i, n := range tc.scores {
				attempts = append(attempts, Attempt{Checks: checks(n)})
				got := Stop(attempts)
				if i < len(tc.scores)-1 && got != "" {
					t.Fatalf("premature stop: %s", got)
				}
				if i == len(tc.scores)-1 && got != tc.stop {
					t.Fatalf("stop %q want %q", got, tc.stop)
				}
			}
		})
	}
}
func TestJudgeNeverQualifiedBySyntheticCases(t *testing.T) {
	cases := make([]Label, 60)
	for i := range cases {
		cases[i] = Label{CaseID: string(rune('a' + i)), Owner: false, Agrees: true}
	}
	if JudgeQualified(cases) {
		t.Fatal("synthetic labels qualified judge")
	}
	for i := range cases {
		cases[i].Owner = true
	}
	if !JudgeQualified(cases) {
		t.Fatal("measured agreement not qualified")
	}
	if JudgeQualified(cases[:49]) {
		t.Fatal("49 cases qualified")
	}
	for i := range cases {
		cases[i].CaseID = "duplicate"
	}
	if JudgeQualified(cases) {
		t.Fatal("duplicate cases qualified")
	}
}
