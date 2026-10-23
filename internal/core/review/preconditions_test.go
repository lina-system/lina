// SPDX-License-Identifier: Apache-2.0
package review

import (
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"strings"
	"testing"
)

func greenPR() antigravity.PullRequest {
	p := antigravity.PullRequest{Number: 7, URL: "https://github.com/owner/repo/pull/7", State: "OPEN", Head: strings.Repeat("a", 40), Branch: "task", Base: "dev", Checks: []antigravity.Check{{Type: "CheckRun", Name: "foundation", Status: "COMPLETED", Conclusion: "SUCCESS"}}}
	r := antigravity.Review{State: "APPROVED"}
	r.Commit.OID = p.Head
	p.Reviews = []antigravity.Review{r}
	return p
}
func TestFailedCheckIsPreconditionAndScopeMissIsNotModelFinding(t *testing.T) {
	p := greenPR()
	p.Checks[0].Conclusion = "FAILURE"
	r := Assess(p.Verify("owner/repo", "task", "dev", p.Head), []string{"src/a.go"}, []string{"src/**"})
	if r.State != "blocked" || r.Cause != "merge_precondition" || r.Verdict != "" || len(r.Findings) != 0 {
		t.Fatalf("CI became a verdict/finding: %+v", r)
	}
	p = greenPR()
	r = Assess(p.Verify("owner/repo", "task", "dev", p.Head), []string{"src/a.go", "outside.txt"}, []string{"src/**"})
	if r.State != "complete" || r.Verdict != "needs_changes" || !strings.Contains(r.Evidence, "outside.txt") || len(r.Findings) != 0 {
		t.Fatalf("scope miss: %+v", r)
	}
	r = Assess(p.Verify("owner/repo", "task", "dev", p.Head), []string{"src/deep/a.go"}, []string{"src/**"})
	if r.State != "" {
		t.Fatalf("in-scope ready delivery rejected: %+v", r)
	}
}
