// SPDX-License-Identifier: Apache-2.0
package review

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/work"
)

// scopeOverflowFile locates a remainder finding when no missed path can itself
// be a finding file. The complete path list stays in the scope evidence.
const scopeOverflowFile = "scope-misses"

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
		return Record{State: "complete", Verdict: "needs_changes", Evidence: string(raw), Findings: scopeFindings(misses)}
	}
	return Record{}
}

// scopeFindings records one P1 per out-of-area path until the finding schema
// is full. Paths that do not fit, and paths that cannot be a finding file,
// stay in the scope evidence. One remainder finding names that overflow so
// the review still finishes.
func scopeFindings(misses []string) []Finding {
	const claim = "Changed path is outside the declared write area."
	var accepted []Finding
	for _, file := range misses {
		f := Finding{ID: "core-scope-" + strconv.Itoa(len(accepted)+1), Severity: "P1", File: file, Claim: claim}
		if findingAccepted(f) {
			accepted = append(accepted, f)
		}
	}
	if len(accepted) == len(misses) && len(accepted) <= maxFindings {
		return accepted
	}
	limit := len(accepted)
	if limit > maxFindings-1 {
		limit = maxFindings - 1
	}
	listed := append([]Finding(nil), accepted[:limit]...)
	file := scopeOverflowFile
	if limit < len(accepted) {
		file = accepted[limit].File
	} else if len(listed) > 0 {
		file = listed[0].File
	}
	more := Finding{ID: "core-scope-more", Severity: "P1", File: file, Claim: scopeOverflowClaim(len(misses), limit)}
	if !findingAccepted(more) {
		more.File = scopeOverflowFile
	}
	return append(listed, more)
}

func scopeOverflowClaim(total, named int) string {
	unit, verb := "paths", "are"
	if total == 1 {
		unit, verb = "path", "is"
	}
	return fmt.Sprintf("%d changed %s %s outside the declared write area. %d are named as findings; the complete list is in the scope evidence.", total, unit, verb, named)
}
