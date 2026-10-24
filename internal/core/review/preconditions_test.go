// SPDX-License-Identifier: Apache-2.0
package review

import (
	"encoding/json"
	"fmt"
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

func TestScopeFindingsStayWithinTheSchemaWhenManyPathsMiss(t *testing.T) {
	files := make([]string, 257)
	for i := range files {
		files[i] = fmt.Sprintf("out/%d.txt", i+1)
	}
	r := Assess(Precondition{}, files, []string{"src/**"})
	raw, err := json.Marshal(map[string]any{"findings": r.Findings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(raw); err != nil || r.State != "complete" || r.Verdict != "needs_changes" || len(r.Findings) != 256 {
		t.Fatalf("scope findings exceed the schema: count=%d err=%v record=%+v", len(r.Findings), err, r.State)
	}
	if r.Findings[0].ID != "core-scope-1" || r.Findings[0].File != "out/1.txt" || r.Findings[254].ID != "core-scope-255" || r.Findings[255].ID != "core-scope-more" || r.Findings[255].File != "out/256.txt" {
		t.Fatalf("remainder finding: %+v", r.Findings[len(r.Findings)-1])
	}
	if !strings.Contains(r.Evidence, `"out/1.txt"`) || !strings.Contains(r.Evidence, `"out/257.txt"`) {
		t.Fatalf("scope evidence dropped a path: %s", r.Evidence)
	}
	exact := make([]string, 256)
	for i := range exact {
		exact[i] = fmt.Sprintf("exact/%d.txt", i+1)
	}
	gotExact := Assess(Precondition{}, exact, []string{"src/**"})
	exactRaw, err := json.Marshal(map[string]any{"findings": gotExact.Findings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(exactRaw); err != nil || len(gotExact.Findings) != 256 || gotExact.Findings[255].ID != "core-scope-256" {
		t.Fatalf("256 paths should stay one finding each: %d %v %+v", len(gotExact.Findings), err, gotExact.Findings)
	}
	db := reviewDB(t)
	rec := Record{Key: Key{Repository: "owner/repo", Number: 11, Head: head('b')}, Task: "task", Receipt: "receipt", Generation: 1}
	if err = New(db).Claim(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.State, rec.Verdict, rec.Evidence, rec.Findings = r.State, r.Verdict, r.Evidence, r.Findings
	finishReview(t, db, rec)
	got, err := New(db).Get(t.Context(), rec.Key)
	if err != nil || got.State != "complete" || got.Verdict != "needs_changes" || len(got.Findings) != 256 {
		t.Fatalf("large scope miss left the claim unfinished: %+v %v", got, err)
	}
}

func TestUnusableScopePathStaysInEvidence(t *testing.T) {
	long := strings.Repeat("p", 4097)
	files := []string{long, `bad\name`, "ok.txt"}
	r := Assess(Precondition{}, files, []string{"src/**"})
	raw, err := json.Marshal(map[string]any{"findings": r.Findings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(raw); err != nil || r.Verdict != "needs_changes" || len(r.Findings) != 2 {
		t.Fatalf("unusable path rejected the scope review: %+v %v", r.Findings, err)
	}
	if r.Findings[0].File != "ok.txt" || r.Findings[1].ID != "core-scope-more" {
		t.Fatalf("findings: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.File == long || f.File == `bad\name` {
			t.Fatalf("unusable path became a finding file: %s", f.File)
		}
	}
	if !strings.Contains(r.Evidence, long) || !strings.Contains(r.Evidence, `bad\\name`) {
		t.Fatalf("evidence lost an unusable path: %s", r.Evidence)
	}
	only := Assess(Precondition{}, []string{`bad\name`}, nil)
	onlyRaw, err := json.Marshal(map[string]any{"findings": only.Findings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(onlyRaw); err != nil || len(only.Findings) != 1 || only.Findings[0].ID != "core-scope-more" || only.Findings[0].File != scopeOverflowFile {
		t.Fatalf("path that cannot be a finding file left no schema-valid result: %+v %v", only.Findings, err)
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
