// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/intakereplay"
)

func TestM2IntakeReplay(t *testing.T) {
	binary := buildLina(t)
	fixture, err := filepath.Abs("../../internal/core/intakereplay/testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cases, rejected := intakereplay.Read(f)
	f.Close()
	if len(rejected) != 0 || len(cases) == 0 {
		t.Fatal(rejected)
	}
	expected := map[string]intakereplay.Case{}
	for _, c := range cases {
		expected[c.ID] = c
	}
	// Like the Core harness, leave room for internal Unix socket names.
	root, err := os.MkdirTemp("", "lina-ir-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	home := filepath.Join(root, "home")
	scratch := filepath.Join(root, "scratch")
	for _, p := range []string{home, scratch} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	run := func(path string, wantExit int, core ...bool) intakereplay.Report {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
		defer cancel()
		args := []string{"intake-replay"}
		if len(core) > 0 && core[0] {
			args = append(args, "--core")
		}
		args = append(args, path)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + scratch, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		raw, err := cmd.Output()
		if wantExit == 0 && err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				t.Logf("stderr: %s", exit.Stderr)
			}
			t.Fatalf("replay: %v %s", err, raw)
		}
		if wantExit != 0 {
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != wantExit {
				t.Fatalf("exit: %v %s", err, raw)
			}
		}
		var report intakereplay.Report
		if err = json.Unmarshal(raw, &report); err != nil {
			t.Fatalf("output: %s: %v", raw, err)
		}
		left, err := os.ReadDir(scratch)
		if err != nil || len(left) != 0 {
			t.Fatalf("Core resources left: %v %v", left, err)
		}
		return report
	}
	report := run(fixture, 0)
	if report.Scored != len(cases) || report.Excluded != 0 || len(report.Results) != 2*len(cases) {
		t.Fatalf("incomplete replay: %+v", report)
	}
	for _, r := range report.Results {
		c, ok := expected[r.ID]
		if !ok || r.Score == nil || !reflect.DeepEqual(*r.Score, c.Expected[r.Router]) {
			t.Fatalf("wrong score: %+v", r)
		}
	}
	production := run(fixture, 0, true)
	if production.Scored != len(cases) || production.Excluded != 0 {
		t.Fatalf("incomplete production replay: %+v", production)
	}
	rules := map[string]int{}
	for _, summary := range production.Summaries {
		if summary.Router != "core-rules" {
			t.Fatal("wrong production router", summary)
		}
		rules[summary.Decider] += summary.Metrics.Picks
		if (summary.Decider == "reply-or-card" || summary.Decider == "offered-option") && summary.Metrics.Passed != summary.Metrics.Cases {
			t.Fatalf("inexact Core rule: %+v", summary)
		}
	}
	for _, rule := range []string{"reply-or-card", "exact-reference", "offered-option", "no-rule"} {
		if rules[rule] == 0 {
			t.Fatal("missing production rule coverage", rule)
		}
	}
	// The same ids are seeded on every case and every router. A shared database
	// would conflict or contaminate these scores. Missing input must fail closed.
	missing := run(filepath.Join(root, "missing.jsonl"), 1)
	if missing.Scored != 0 || missing.ExclusionReasons["missing_file"] != 1 {
		t.Fatalf("missing file scored: %+v", missing)
	}
	if len(report.Summaries) < 8 {
		t.Fatal("missing set/decider breakdown", report.Summaries)
	}
}
