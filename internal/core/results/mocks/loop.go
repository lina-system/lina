// SPDX-License-Identifier: Apache-2.0

// Package mocks checks drawn pages and owns their bounded redraw evaluation.
package mocks

import "github.com/thisisjun786/lina/kit/result"

var Dimensions = []string{"elements", "controls", "tokens", "network", "errors", "layout_390", "layout_1280"}

type Attempt struct {
	Fragment string             `json:"fragment"`
	Checks   []result.MockCheck `json:"checks"`
}

func score(a Attempt) int {
	n := 0
	for _, c := range a.Checks {
		if c.Passed {
			n++
		}
	}
	return n
}
func Stop(attempts []Attempt) string {
	if len(attempts) == 0 {
		return ""
	}
	current := score(attempts[len(attempts)-1])
	if current == len(Dimensions) {
		return "all_passed"
	}
	if len(attempts) > 1 && current <= score(attempts[len(attempts)-2]) {
		return "no_improvement"
	}
	if len(attempts) >= 4 {
		return "redraw_limit"
	}
	return ""
}

type Label struct {
	CaseID        string
	Owner, Agrees bool
}

// Synthetic requests never qualify a judge. The conservative qualification bar
// requires at least 50 distinct owner-labelled cases, all with measured agreement.
func JudgeQualified(labels []Label) bool {
	seen := map[string]bool{}
	for _, l := range labels {
		if !l.Owner || !l.Agrees || l.CaseID == "" || seen[l.CaseID] {
			return false
		}
		seen[l.CaseID] = true
	}
	return len(seen) >= 50
}
