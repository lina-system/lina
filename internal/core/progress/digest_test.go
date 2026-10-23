// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"strings"
	"testing"
)

func TestDigestBoundsCountsAndKeepsWaitingFirst(t *testing.T) {
	entries := Compute([]Unit{{ID: "hidden", Kind: "project", Name: "Build\na report", Active: true}})
	for _, n := range []int{0, 2, 5, 8} {
		waiting := make([]string, n)
		for i := range waiting {
			waiting[i] = "Decide\r\nnow"
		}
		d := Digest(waiting, entries)
		if len(d.Lines) != min(5, n+1) || d.Total != n+1 || d.More != max(0, n+1-5) {
			t.Fatalf("bad bound/count: %+v", d)
		}
		for i, line := range d.Lines {
			if strings.ContainsAny(line, "\n\r") || strings.Contains(line, "hidden") || strings.Contains(line, "%") {
				t.Fatalf("unsafe digest: %q", line)
			}
			if i < n && line != "Decide now" {
				t.Fatalf("waiting order lost: %q", line)
			}
		}
	}
}

func TestDigestPreservesExecutionStateWithoutPlanOutcome(t *testing.T) {
	rows := Compute([]Unit{{ID: "private-session", Kind: "project", Name: "Fixture job", State: "failed", Landed: true}})
	digest := Digest(nil, rows)
	if len(digest.Lines) != 1 || !strings.Contains(digest.Lines[0], "failed: Fixture job") || rows[0].Line.Stage != "unknown" || rows[0].Line.Percent != nil {
		t.Fatalf("execution state lost or became a plan verdict: %+v %+v", digest, rows)
	}
}
