// SPDX-License-Identifier: Apache-2.0
package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
)

func greenPR() antigravity.PullRequest {
	p := antigravity.PullRequest{Number: 7, URL: "https://github.com/owner/repo/pull/7", State: "OPEN", Head: strings.Repeat("a", 40), Branch: "task", Base: "dev", Checks: []antigravity.Check{{Type: "CheckRun", Name: "foundation", Status: "COMPLETED", Conclusion: "SUCCESS"}}}
	r := antigravity.Review{State: "APPROVED"}
	r.Commit.OID = p.Head
	p.Reviews = []antigravity.Review{r}
	return p
}

func assessPR(t *testing.T, p antigravity.PullRequest, files, areas []string) Record {
	t.Helper()
	blocked, failures := p.Readiness("owner/repo", "task", "dev", p.Head)
	return Assess(Precondition{Blocked: blocked, Failures: failures}, files, areas)
}

func TestFailedCheckAndScopeMissAreCoreComputedP1Findings(t *testing.T) {
	p := greenPR()
	p.Checks[0].Conclusion = "FAILURE"
	r := assessPR(t, p, []string{"src/a.go"}, []string{"src/**"})
	if r.State != "blocked" || r.Cause != "merge_precondition" || r.Verdict != "" || r.Evidence == "" {
		t.Fatalf("failed check is not a merge precondition: %+v", r)
	}
	if len(r.Findings) != 1 || r.Findings[0].Severity != "P1" || r.Findings[0].ID != "core-check-1" || r.Findings[0].File != CheckFile || !strings.Contains(r.Findings[0].Claim, "foundation") {
		t.Fatalf("failed check is not a P1 finding: %+v", r.Findings)
	}

	p = greenPR()
	r = assessPR(t, p, []string{"src/a.go", "outside.txt"}, []string{"src/**"})
	if r.State != "complete" || r.Verdict != "needs_changes" || !strings.Contains(r.Evidence, "outside.txt") {
		t.Fatalf("scope miss: %+v", r)
	}
	if len(r.Findings) != 1 || r.Findings[0].Severity != "P1" || r.Findings[0].ID != "core-scope-1" || r.Findings[0].File != "outside.txt" {
		t.Fatalf("scope miss is not a P1 finding: %+v", r.Findings)
	}
	raw, err := json.Marshal(map[string]any{"findings": r.Findings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(raw); err != nil {
		t.Fatalf("core finding rejected by the finding schema: %v", err)
	}

	if r = assessPR(t, p, []string{"src/deep/a.go"}, []string{"src/**"}); r.State != "" {
		t.Fatalf("in-scope ready delivery rejected: %+v", r)
	}
}

func TestWaitsAndIdentityMismatchesStayFindingsFree(t *testing.T) {
	pending := greenPR()
	pending.Checks[0].Status = "IN_PROGRESS"
	pending.Checks[0].Conclusion = ""
	unreviewed := greenPR()
	unreviewed.Reviews = nil
	moved := greenPR()
	moved.Head = strings.Repeat("c", 40)
	for name, p := range map[string]antigravity.PullRequest{"pending": pending, "unreviewed": unreviewed, "moved": moved} {
		t.Run(name, func(t *testing.T) {
			r := assessPR(t, p, []string{"src/a.go"}, []string{"src/**"})
			if r.State != "blocked" || r.Cause != "merge_precondition" || r.Verdict != "" || len(r.Findings) != 0 || r.Evidence == "" {
				t.Fatalf("wait became a finding: %+v", r)
			}
		})
	}
}
