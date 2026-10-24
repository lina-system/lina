// SPDX-License-Identifier: Apache-2.0
package review

import (
	"encoding/json"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/work"
)

// Precondition is Core's observed readiness for the delivered head. Blocked
// carries the reason the head is not ready for a manager judgment. Failures
// names the required checks that reached a terminal negative conclusion: they
// are evidence Core records, never a reason to call the model.
type Precondition struct {
	Blocked  error
	Failures []string
}

// CheckFile locates a failed-check finding. GitHub reports no failing path, so
// Core points at the workflow definitions that decide the check.
const CheckFile = ".github/workflows"

// Assess is Core's mechanical boundary, not a model finding classifier. The
// existing GitHub reader requires all observed checks and current-head review.
// An empty State means the manager may check the definition, not acceptance.
// Core-computed findings use stable ids so a return and a close name the same
// evidence for the same head.
func Assess(pre Precondition, files, areas []string) Record {
	if pre.Blocked != nil {
		r := Record{State: "blocked", Cause: "merge_precondition", Evidence: pre.Blocked.Error()}
		for i, name := range pre.Failures {
			r.Findings = append(r.Findings, Finding{ID: "core-check-" + strconv.Itoa(i+1), Severity: "P1", File: CheckFile, Claim: "Required check failed at the delivered head: " + name})
		}
		return r
	}
	var misses []string
	for _, file := range files {
		allowed := false
		for _, area := range areas {
			allowed = allowed || work.AreaContains(area, file)
		}
		if !allowed {
			misses = append(misses, file)
		}
	}
	if len(misses) > 0 {
		raw, _ := json.Marshal(map[string]any{"scope_misses": misses})
		r := Record{State: "complete", Verdict: "needs_changes", Evidence: string(raw)}
		for i, file := range misses {
			r.Findings = append(r.Findings, Finding{ID: "core-scope-" + strconv.Itoa(i+1), Severity: "P1", File: file, Claim: "Changed path is outside the declared write area."})
		}
		return r
	}
	return Record{}
}
