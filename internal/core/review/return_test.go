// SPDX-License-Identifier: Apache-2.0
package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReturnKeepsOnlyBlockingFindingIDs(t *testing.T) {
	r := Record{State: "complete", Verdict: "needs_changes", Evidence: "Missing required behavior.", Findings: []Finding{
		{ID: "critical", Severity: "P0", File: "src/a.go", Claim: "Broken invariant."},
		{ID: "minor", Severity: "P2", File: "src/a.go", Claim: "Optional improvement."},
		{ID: "major", Severity: "P1", File: "src/a.go", Claim: "Missing behavior."},
		{ID: "nit", Severity: "P3", File: "src/a.go", Claim: "Naming."},
	}}
	got, err := Return(r)
	if err != nil || len(got.Findings) != 2 || got.Findings[0].ID != "critical" || got.Findings[1].ID != "major" {
		t.Fatalf("return=%+v err=%v", got, err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), `"minor"`) || strings.Contains(string(raw), `"nit"`) || len(r.Findings) != 4 {
		t.Fatal("low-priority ids returned or source findings changed", string(raw))
	}
}

func TestReturnRequiresWorkDefinitionMiss(t *testing.T) {
	for _, r := range []Record{{State: "blocked", Cause: "merge_precondition"}, {State: "checking"}, {State: "complete", Verdict: "verified", Evidence: "Pass"}, {State: "unverified"}} {
		if _, err := Return(r); err == nil {
			t.Fatalf("non-definition failure became a return: %+v", r)
		}
	}
	if _, err := Return(Record{State: "complete", Verdict: "needs_changes", Evidence: "Scope missed"}); err != nil {
		t.Fatal(err)
	}
}
