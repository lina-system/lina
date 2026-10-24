// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func TestDeriveSerializesOverlappingAreas(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b    string
		overlap bool
	}{
		{"src/**", "src/auth/**", true},
		{"src/*", "src/main.go", true},
		{"src", "src/auth", true},
		{"./src/", "src/auth/**", true},
		{"src/*.go", "src/main.*", true},
		{"**/auth/*.go", "src/auth/main.go", true},
		{"**/auth/*.go", "auth/main.go", true},
		{"src/[a-c].go", "src/[c-f].go", true},
		{"src/?.go", "src/x.go", true},
		{`src/[\d].go`, "src/d.go", true},
		{`src/[\d].go`, "src/1.go", false},
		{`src/\*.go`, "src/other.go", false},
		{"src/[^a-c].go", "src/d.go", true},
		{"src/[^a-c].go", "src/a.go", false},
		{"src/**", "docs/**", false},
		{"src", "src-other", false},
		{"src/*.go", "src/*.md", false},
		{"src/[a-c].go", "src/[d-f].go", false},
	} {
		t.Run(tc.a+"_"+tc.b, func(t *testing.T) {
			for _, areas := range [][2]string{{tc.a, tc.b}, {tc.b, tc.a}} {
				a, b := readyTask("a"), readyTask("b")
				a.Area, b.Area = areas[0], areas[1]
				p, err := Derive(State{Goals: []Goal{oneGoal(a, b)}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
				if err != nil {
					t.Fatal(err)
				}
				want := 2
				if tc.overlap {
					want = 1
				}
				if len(p.Actions) != want {
					t.Fatalf("areas %q: want %d assignments, got %+v", areas, want, p)
				}
				if tc.overlap && (len(p.Waits) != 1 || p.Waits[0].Reason != "area") {
					t.Fatalf("missing area wait: %+v", p)
				}
			}
		})
	}
}

func TestDeriveHoldsOverlappingAreaAcrossGoalsUntilLanding(t *testing.T) {
	t.Parallel()
	a, b := readyTask("a"), readyTask("b")
	a.Area, b.Area = "src/**", "src/auth/**"
	a.Stage = TaskBuilding
	g, other := oneGoal(a), oneGoal(b)
	other.ID = "other"
	state := State{Goals: []Goal{g, other}, Nodes: []Node{readyNode("n")}}
	p, err := Derive(state, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 0 {
		t.Fatalf("overlapped an existing worker: %+v", p)
	}
	state.Goals[0].Tasks[0].Stage = TaskMerged
	state.Goals[0].Tasks[0].Merge = Proof{Revision: "merged", Ref: "merge", Passed: true}
	p, err = Derive(state, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 2 || p.Actions[1].TaskID != "b" || p.Actions[1].Kind != Assign {
		t.Fatalf("landing did not release area: %+v", p)
	}
	state.Goals[0].Tasks[0] = a
	state.Goals[1].Scope.Repositories = []string{"another-repo"}
	state.Goals[1].Tasks[0].Repository = "another-repo"
	p, err = Derive(state, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].TaskID != "b" {
		t.Fatalf("unrelated repository blocked: %+v", p)
	}
}
