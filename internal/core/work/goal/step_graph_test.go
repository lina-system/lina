// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func graphFixture(t *testing.T) (*Engine, *testStore) {
	t.Helper()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal()
	g.Conversation = "conversation"
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	return e, db
}
func graphStep(id, name string) Step {
	t := readyTask(id)
	return Step{ID: id, Name: name, Done: t.Done, Repository: t.Repository, Base: t.Base, Area: t.Area, Worker: t.Worker, Resources: t.Resources, DependsOn: []string{}}
}
func submitGraph(t *testing.T, e *Engine, revision uint64, steps []Step) (StepGraph, error) {
	t.Helper()
	return e.SubmitStepGraph(t.Context(), "g", "person", "agent", "conversation", revision, steps)
}
func TestStepGraphRefusalsAreAtomic(t *testing.T) {
	cases := []struct {
		name string
		code GraphError
		edit func([]Step) []Step
	}{
		{"cycle", GraphCycle, func(s []Step) []Step {
			s[0].DependsOn = []string{"second"}
			s[1].DependsOn = []string{"first"}
			return s
		}},
		{"unknown", GraphUnknownStep, func(s []Step) []Step { s[1].DependsOn = []string{"absent"}; return s }},
		{"duplicate", GraphUnknownStep, func(s []Step) []Step { s[1].ID = s[0].ID; return s }},
		{"absolute area", GraphInvalidArea, func(s []Step) []Step { s[0].Area = "/tmp/result"; return s }},
		{"parent area", GraphInvalidArea, func(s []Step) []Step { s[0].Area = "../result"; return s }},
		{"bad glob", GraphInvalidArea, func(s []Step) []Step { s[0].Area = "src/["; return s }},
		{"overlap", GraphInvalidArea, func(s []Step) []Step { s[0].Area = "src/**"; s[1].Area = "src/file"; return s }},
		{"missing name", GraphInvalidName, func(s []Step) []Step { s[0].Name = " "; return s }},
		{"wide", GraphInvalidName, func(s []Step) []Step { s[0].Name = strings.Repeat("가", 13); return s }},
		{"issue", GraphInvalidName, func(s []Step) []Step { s[0].Name = "Fix #762"; return s }},
		{"pr", GraphInvalidName, func(s []Step) []Step { s[0].Name = "Read PR 12"; return s }},
		{"branch", GraphInvalidName, func(s []Step) []Step { s[0].Name = "Merge dev"; return s }},
		{"path", GraphInvalidName, func(s []Step) []Step { s[0].Name = "Update src/file.go"; return s }},
		{"check", GraphInvalidName, func(s []Step) []Step { s[0].Name = "Run foundation"; return s }},
		{"id", GraphInvalidName, func(s []Step) []Step { s[0].Name = "Finish first"; return s }},
		{"out of scope", GraphInvalid, func(s []Step) []Step { s[0].Repository = "other"; return s }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := graphFixture(t)
			original := []Step{graphStep("first", "숙소 고르기"), graphStep("second", "이동 준비")}
			saved, err := submitGraph(t, e, 0, original)
			if err != nil {
				t.Fatal(err)
			}
			before, err := e.journal.Records(t.Context(), "goal")
			if err != nil {
				t.Fatal(err)
			}
			_, err = submitGraph(t, e, 1, tc.edit(original))
			if !errors.Is(err, tc.code) {
				t.Fatalf("got %v want %v", err, tc.code)
			}
			after, err := e.journal.Records(t.Context(), "goal")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed goal: %v", err)
			}
			got, err := e.ReadStepGraph(t.Context(), "g", "person", "agent", "conversation")
			if err != nil || !reflect.DeepEqual(got, saved) {
				t.Fatalf("refusal changed receipt: %+v %v", got, err)
			}
		})
	}
}
func TestStepGraphRevisionReplayAndRestart(t *testing.T) {
	e, db := graphFixture(t)
	steps := []Step{graphStep("first", strings.Repeat("가", 12)), graphStep("second", strings.Repeat("e\u0301", 24))}
	steps[1].DependsOn = []string{"first"}
	saved, err := submitGraph(t, e, 0, steps)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := submitGraph(t, e, 0, steps)
	if err != nil || !reflect.DeepEqual(saved, retried) {
		t.Fatalf("retry: %+v %v", retried, err)
	}
	steps[0].Name = "로그인 화면 수정"
	saved, err = submitGraph(t, e, 1, steps)
	if err != nil || saved.Revision != 2 {
		t.Fatalf("revision: %+v %v", saved, err)
	}
	if _, err = submitGraph(t, e, 0, steps); !errors.Is(err, GraphStaleRevision) {
		t.Fatalf("stale: %v", err)
	}
	if err = e.journal.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.ReadStepGraph(t.Context(), "g", "person", "agent", "conversation")
	if err != nil || !reflect.DeepEqual(saved, got) {
		t.Fatalf("restart: %+v %v", got, err)
	}
	for _, identity := range [][3]string{{"other", "agent", "conversation"}, {"person", "other", "conversation"}, {"person", "agent", "other"}} {
		if _, err = restarted.ReadStepGraph(t.Context(), "g", identity[0], identity[1], identity[2]); !errors.Is(err, GraphUnavailable) {
			t.Fatal("foreign read", err)
		}
		if _, err = restarted.SubmitStepGraph(t.Context(), "g", identity[0], identity[1], identity[2], 2, steps); !errors.Is(err, GraphUnavailable) {
			t.Fatal("foreign write", err)
		}
	}
}
func TestStepGraphRevisesExecutableTasksAndProtectsStartedWork(t *testing.T) {
	e, _ := graphFixture(t)
	steps := []Step{graphStep("first", "Create parser"), graphStep("second", "Add validation")}
	steps[1].DependsOn = []string{"first"}
	if _, err := submitGraph(t, e, 0, steps); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := e.Snapshot(t.Context())
	if err != nil || len(state.Actions) != 1 || state.Actions[0].TaskID != "first" {
		t.Fatalf("frontier: %+v %v", state, err)
	}
	action, err := e.Claim(t.Context(), state.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	steps[0].Name = "Build parser"
	if _, err = submitGraph(t, e, 1, steps); err != nil {
		t.Fatal("rename started", err)
	}
	// Renaming must not stale or replace the actual worker's claim.
	if err = e.Report(t.Context(), action.ID, 1, Observation{Status: OutcomeRunning, Handle: "worker"}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]Step) []Step{
		func(s []Step) []Step { s = s[1:]; s[0].DependsOn = nil; return s },
		func(s []Step) []Step { s[0].Done = "different work"; return s },
		func(s []Step) []Step { s[0].DependsOn = []string{"second"}; s[1].DependsOn = nil; return s },
	} {
		var proposal []Step
		json.Unmarshal(encode(steps), &proposal)
		if _, err = submitGraph(t, e, 2, mutate(proposal)); !errors.Is(err, GraphStartedStep) {
			t.Fatalf("started change: %v", err)
		}
	}
	steps[1].Done = "new validation"
	steps = append(steps, graphStep("third", "Write examples"))
	if _, err = submitGraph(t, e, 2, steps); err != nil {
		t.Fatal("pending edit/add", err)
	}
	state, err = e.Snapshot(t.Context())
	if err != nil || len(state.Goals[0].Tasks) != 3 || state.Goals[0].Tasks[1].Done != "new validation" || state.Goals[0].Tasks[0].Attempt != 1 {
		t.Fatalf("executable state: %+v %v", state, err)
	}
}
func TestStepNamesMeasureGraphemesAndRejectTechnicalTokens(t *testing.T) {
	for _, name := range []string{"숙소 고르기", "로그인 화면 수정", strings.Repeat("👩‍💻", 12), strings.Repeat("e\u0301", 24), strings.Repeat("x", 24)} {
		if !validStepName(name, nil) {
			t.Errorf("refused %q", name)
		}
	}
	for _, name := range []string{strings.Repeat("x", 25), strings.Repeat("👩‍💻", 13), "codex/login", "Fix LN-762", "Edit README.md", "id: abc", "abc_def", "Fix\nlogin", "Fix \u202eabc", "Fix \x1b[1m", "Fix deadbeef"} {
		if validStepName(name, nil) {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestStepGraphReplacesUnclaimedWorkAndRenamesFinishedWork(t *testing.T) {
	e, _ := graphFixture(t)
	steps := []Step{graphStep("first", "Create parser")}
	if _, err := submitGraph(t, e, 0, steps); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Snapshot(t.Context())
	steps[0].Done = "Check the replacement parser"
	if _, err := submitGraph(t, e, 1, steps); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Claim(t.Context(), before.Actions[0].ID); err == nil {
		t.Fatal("old queued action remained claimable")
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Snapshot(t.Context())
	if after.Goals[0].Tasks[0].Attempt != 1 || after.Goals[0].Tasks[0].Done != steps[0].Done {
		t.Fatalf("replacement lost its definition or spent attempt: %+v", after.Goals[0])
	}
	// A completed goal can still correct a display name without reopening work.
	if err := e.journal.Update(t.Context(), Transition{ID: "fixture-finished", Kind: "fixture", At: 100}, func(v *View) error {
		g, err := loadGoal(v, "g")
		if err != nil {
			return err
		}
		g.State = GoalDone
		g.Tasks[0].Stage = TaskDone
		return put(v, "goal", g.ID, g)
	}); err != nil {
		t.Fatal(err)
	}
	steps[0].Name = "Prepare parser"
	if _, err := submitGraph(t, e, 2, steps); err != nil {
		t.Fatal("finished rename", err)
	}
	after, _ = e.Snapshot(t.Context())
	if after.Goals[0].State != GoalDone || after.Goals[0].Tasks[0].Stage != TaskDone {
		t.Fatal("rename reopened finished work")
	}
}

func TestStepGraphConcurrentRevisionsHaveOneWinner(t *testing.T) {
	e, _ := graphFixture(t)
	steps := []Step{graphStep("first", "Create parser")}
	if _, err := submitGraph(t, e, 0, steps); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"Build parser", "Prepare parser"} {
		go func() {
			<-start
			_, err := e.SubmitStepGraph(t.Context(), "g", "person", "agent", "conversation", 1, []Step{graphStep("first", name)})
			results <- err
		}()
	}
	close(start)
	successes, stale := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, GraphStaleRevision):
			stale++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("lost update: success=%d stale=%d", successes, stale)
	}
}

func TestStepGraphKeepsRepairProvenance(t *testing.T) {
	e, _ := graphFixture(t)
	steps := []Step{graphStep("first", "Create parser"), graphStep("second", "Repair parser")}
	if _, err := submitGraph(t, e, 0, steps); err != nil {
		t.Fatal(err)
	}
	if err := e.journal.Update(t.Context(), Transition{ID: "fixture-repair", Kind: "fixture", At: 100}, func(v *View) error {
		g, err := loadGoal(v, "g")
		if err != nil {
			return err
		}
		g.Tasks[0].RepairedBy = "second"
		g.Tasks[1].Repairs = "first"
		return put(v, "goal", g.ID, g)
	}); err != nil {
		t.Fatal(err)
	}
	steps[1].Done = "Check corrected behavior"
	if _, err := submitGraph(t, e, 1, steps); err != nil {
		t.Fatal(err)
	}
	state, err := e.Snapshot(t.Context())
	if err != nil || state.Goals[0].Tasks[1].Repairs != "first" || state.Goals[0].Tasks[0].RepairedBy != "second" {
		t.Fatalf("lost repair provenance: %+v %v", state, err)
	}
}

func TestStepNamesCannotHideKnownTokensWithJoiners(t *testing.T) {
	for _, name := range []string{"Run foun\u200ddation", "Run FOUNDATION"} {
		if validStepName(name, []string{"foundation"}) {
			t.Errorf("accepted hidden check name: %q", name)
		}
	}
}
