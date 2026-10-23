// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

// TestStageListNumbersByLongestChain generates step graphs and checks that each
// stage holds exactly the steps of one longest-chain depth.
func TestStageListNumbersByLongestChain(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	for n := range 300 {
		steps := randomSteps(r, 1+r.IntN(24))
		list, err := Stages([]Revision{{Steps: steps}}, goal.GoalRunning, design.Korean)
		if err != nil {
			t.Fatalf("graph %d: %v", n, err)
		}
		if len(list) != depthCount(steps)+1 {
			t.Fatalf("graph %d: %d entries for %d depths: %+v", n, len(list), depthCount(steps), list)
		}
		want := depths(steps)
		blocked := map[string][]string{}
		for _, step := range steps {
			blocked[step.ID] = step.DependsOn
		}
		got := map[string]int{}
		for i, entry := range list {
			if i == len(list)-1 {
				if entry.Kind != KindChecking {
					t.Fatalf("graph %d: last entry is %q", n, entry.Kind)
				}
				continue
			}
			if entry.Number != i+1 || entry.Kind != KindStage {
				t.Fatalf("graph %d: entry %d = %+v", n, i, entry)
			}
			want := StepNext
			for _, step := range entry.Steps {
				if len(blocked[step.ID]) == 0 {
					want = StepWaiting
				}
			}
			if entry.State != want {
				t.Fatalf("graph %d: stage %d state %q, want %q", n, entry.Number, entry.State, want)
			}
			if len(entry.Steps) == 0 {
				t.Fatalf("graph %d: empty stage %d", n, entry.Number)
			}
			for _, step := range entry.Steps {
				if got[step.ID] != 0 {
					t.Fatalf("graph %d: step %s listed twice", n, step.ID)
				}
				got[step.ID] = entry.Number
			}
		}
		for _, step := range steps {
			if got[step.ID] != want[step.ID] {
				t.Fatalf("graph %d: step %s at stage %d, want depth %d: %+v", n, step.ID, got[step.ID], want[step.ID], list)
			}
			for _, blocker := range step.DependsOn {
				if got[step.ID] <= got[blocker] {
					t.Fatalf("graph %d: step %s at %d not after blocker %s at %d", n, step.ID, got[step.ID], blocker, got[blocker])
				}
			}
		}
	}
}

// TestStageListCollapsesParallelSteps checks that a parallel group is one entry
// whose label names its first step and the number of the others.
func TestStageListCollapsesParallelSteps(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	for n := range 300 {
		steps := randomSteps(r, 2+r.IntN(30))
		for i := range steps {
			if r.IntN(2) == 0 {
				steps[i].StartedAt = time.Unix(int64(r.IntN(1000)), 0)
			}
		}
		for _, language := range []design.Language{design.Korean, design.English} {
			list, err := Stages([]Revision{{Steps: steps}}, goal.GoalRunning, language)
			if err != nil {
				t.Fatalf("graph %d: %v", n, err)
			}
			seen := map[int]bool{}
			for _, entry := range list {
				if entry.Kind != KindStage {
					continue
				}
				if seen[entry.Number] {
					t.Fatalf("graph %d: stage %d split into two entries", n, entry.Number)
				}
				seen[entry.Number] = true
				first := firstStep(entry.Steps)
				want := first.Name
				if len(entry.Steps) > 1 {
					want = fmt.Sprintf(design.Text(language, "stage.more"), first.Name, len(entry.Steps)-1)
				}
				if entry.Label != want {
					t.Fatalf("graph %d: stage %d label %q, want %q", n, entry.Number, entry.Label, want)
				}
			}
		}
	}
}

// TestStageListKeepsDoneStages replays generated revision sequences and checks
// that a done stage never reopens or renumbers, and that a step admitted later
// lands at least one stage past the leading run of done stages.
func TestStageListKeepsDoneStages(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 32))
	for n := range 200 {
		steps := randomSteps(r, 1+r.IntN(8))
		revisions := []Revision{{Steps: cloneSteps(steps)}}
		before, err := Stages(revisions, goal.GoalRunning, design.Korean)
		if err != nil {
			t.Fatalf("graph %d: %v", n, err)
		}
		nextID := len(steps)
		for round := range 1 + r.IntN(4) {
			current := cloneSteps(steps)
			// Land the first open numbered stage, as the ledger does when its
			// last step lands.
			if stage, ok := firstOpenStage(before); ok {
				for i := range current {
					if stage[current[i].ID] {
						current[i].State = StepDone
					}
				}
			}
			// Admit later steps, some blocked by steps already there.
			for range r.IntN(3) {
				step := Step{ID: fmt.Sprintf("step-%02d", nextID), Name: fmt.Sprintf("추가 %d", nextID), State: StepWaiting}
				nextID++
				for j := range current {
					if r.IntN(3) == 0 {
						step.DependsOn = append(step.DependsOn, current[j].ID)
					}
				}
				current = append(current, step)
			}
			// Change the blockers of a step that has not started.
			for range r.IntN(2) {
				i := r.IntN(len(current))
				if current[i].State != StepWaiting {
					continue
				}
				current[i].DependsOn = nil
				for j := range i {
					if r.IntN(4) == 0 {
						current[i].DependsOn = append(current[i].DependsOn, current[j].ID)
					}
				}
			}
			steps = current
			revisions = append(revisions, Revision{Steps: cloneSteps(current)})
			after, err := Stages(revisions, goal.GoalRunning, design.Korean)
			if err != nil {
				t.Fatalf("graph %d round %d: %v", n, round, err)
			}
			checkDoneStages(t, n, before, after, steps)
			before = after
		}
	}
}

func checkDoneStages(t *testing.T, n int, before, after []Stage, steps []Step) {
	t.Helper()
	beforeBy := numberedStages(before)
	afterBy := numberedStages(after)
	run := 0
	for beforeBy[run+1].State == StepDone {
		run++
	}
	for d := 1; d <= run; d++ {
		old, ok := beforeBy[d]
		if !ok {
			t.Fatalf("graph %d: stage %d missing before", n, d)
		}
		now, ok := afterBy[d]
		if !ok {
			t.Fatalf("graph %d: done stage %d disappeared", n, d)
		}
		if !reflect.DeepEqual(stepIDs(old.Steps), stepIDs(now.Steps)) {
			t.Fatalf("graph %d: done stage %d changed steps %v -> %v", n, d, stepIDs(old.Steps), stepIDs(now.Steps))
		}
		if now.State != StepDone {
			t.Fatalf("graph %d: done stage %d reopened as %q", n, d, now.State)
		}
	}
	known := map[string]bool{}
	for _, entry := range before {
		for _, step := range entry.Steps {
			known[step.ID] = true
		}
	}
	stageOf := map[string]int{}
	for number := 1; ; number++ {
		entry, ok := afterBy[number]
		if !ok {
			break
		}
		for _, step := range entry.Steps {
			stageOf[step.ID] = number
		}
	}
	for number := range len(afterBy) {
		if _, ok := afterBy[number+1]; !ok {
			t.Fatalf("graph %d: stage numbers are not contiguous: %v", n, afterBy)
		}
	}
	for _, entry := range after {
		for _, step := range entry.Steps {
			if !known[step.ID] && entry.Number <= run {
				t.Fatalf("graph %d: admitted step %s placed at stage %d inside the done run %d", n, step.ID, entry.Number, run)
			}
		}
	}
	blockers := map[string][]string{}
	for _, step := range steps {
		blockers[step.ID] = step.DependsOn
	}
	for id, number := range stageOf {
		for _, blocker := range blockers[id] {
			if number <= stageOf[blocker] {
				t.Fatalf("graph %d: step %s at %d not after blocker %s at %d", n, id, number, blocker, stageOf[blocker])
			}
		}
	}
}

// TestStageListIsDeterministic checks that the same ledger gives the same list
// whatever order the steps of each revision arrive in.
func TestStageListIsDeterministic(t *testing.T) {
	r := rand.New(rand.NewPCG(41, 42))
	for n := range 200 {
		revisions := randomRevisions(r)
		want, err := Stages(revisions, goal.GoalRunning, design.Korean)
		if err != nil {
			t.Fatalf("graph %d: %v", n, err)
		}
		shuffled := make([]Revision, len(revisions))
		for i, revision := range revisions {
			steps := cloneSteps(revision.Steps)
			r.Shuffle(len(steps), func(a, b int) { steps[a], steps[b] = steps[b], steps[a] })
			shuffled[i] = Revision{Steps: steps}
		}
		got, err := Stages(shuffled, goal.GoalRunning, design.Korean)
		if err != nil {
			t.Fatalf("graph %d: %v", n, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("graph %d: step order changed the list:\n%+v\n%+v", n, want, got)
		}
		first, _ := json.Marshal(want)
		second, _ := json.Marshal(got)
		if string(first) != string(second) {
			t.Fatalf("graph %d: JSON differs: %s / %s", n, first, second)
		}
	}
}

func TestStageListSpecialEntries(t *testing.T) {
	list, err := Stages(nil, goal.GoalDefining, design.Korean)
	if err != nil || len(list) != 1 || list[0].Kind != KindDesign || list[0].Label != "설계" || list[0].State != StepNext {
		t.Fatalf("defining list = %+v, %v", list, err)
	}
	done := []Revision{{Steps: []Step{{ID: "a", Name: "A", State: StepDone}}}}
	list, err = Stages(done, goal.GoalDone, design.Korean)
	if err != nil || len(list) != 1 || list[0].Kind != KindDone || list[0].Label != "끝" || list[0].State != StepDone {
		t.Fatalf("done list = %+v, %v", list, err)
	}
	working := []Revision{{Steps: []Step{{ID: "a", Name: "A", State: StepWorking}}}}
	list, err = Stages(working, goal.GoalChecking, design.Korean)
	if err != nil || len(list) != 2 || list[1].Kind != KindChecking || list[1].State != StepWorking || list[1].Label != "확인" {
		t.Fatalf("checking list = %+v, %v", list, err)
	}
	list, err = Stages(working, goal.GoalRunning, design.Korean)
	if err != nil || len(list) != 2 || list[1].Kind != KindChecking || list[1].State != StepNext {
		t.Fatalf("running list = %+v, %v", list, err)
	}
	if list[0].State != StepWorking || list[0].Label != "A" {
		t.Fatalf("started stage = %+v", list[0])
	}
}

func TestStageListRejectsInvalidGraph(t *testing.T) {
	cases := map[string][]Step{
		"cycle":   {{ID: "a", Name: "A", DependsOn: []string{"b"}}, {ID: "b", Name: "B", DependsOn: []string{"a"}}},
		"unknown": {{ID: "a", Name: "A", DependsOn: []string{"missing"}}},
		"duplicate": {
			{ID: "a", Name: "A"}, {ID: "a", Name: "B"},
		},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Stages([]Revision{{Steps: steps}}, goal.GoalRunning, design.Korean); !errors.Is(err, ErrStageGraph) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestGoalStagesReadsTheLedger checks that the list comes from the committed
// step graph revisions and the observed task states, and repeats after a restart.
func TestGoalStagesReadsTheLedger(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	repo, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	engine, err := goal.NewEngine(goal.Config{DB: db, Records: stageDefinitions{repo}, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := goal.Goal{ID: "g", Person: "person", Agent: "agent", Conversation: "conversation", Summary: "Prepare the trip", Done: "The trip is booked", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"repo"}, Includes: "plan.md"}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "The trip is booked", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "repo"}}}}
	if err = engine.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	step := func(id, name string, blockers ...string) goal.Step {
		return goal.Step{ID: id, Name: name, Done: "Produce " + name, Repository: "repo", Base: "dev", Area: id + ".txt", Worker: "antigravity", Resources: goal.Resources{CPU: 0.01, MemoryMB: 1, DiskMB: 1}, DependsOn: blockers}
	}
	if _, err = engine.SubmitStepGraph(t.Context(), "g", "person", "agent", "conversation", 0, []goal.Step{step("stay", "숙소 고르기"), step("move", "이동 준비", "stay"), step("pack", "짐 싸기")}); err != nil {
		t.Fatal(err)
	}
	read := func() []Stage {
		t.Helper()
		var list []Stage
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var err error
			list, err = GoalStages(t.Context(), tx, "g", "person", design.Korean)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return list
	}
	list := read()
	if len(list) != 3 || list[0].Number != 1 || list[1].Number != 2 || list[2].Kind != KindChecking {
		t.Fatalf("stage list = %+v", list)
	}
	if len(list[0].Steps) != 2 || list[0].Label != firstLabel(list[0]) || !strings.HasSuffix(list[0].Label, " 외 1") {
		t.Fatalf("parallel stage = %+v", list[0])
	}
	want := map[string]StepState{"stay": StepWaiting, "pack": StepWaiting, "move": StepNext}
	for _, entry := range list[:2] {
		for _, step := range entry.Steps {
			if step.State != want[step.ID] {
				t.Fatalf("step %s shows %q, want %q", step.ID, step.State, want[step.ID])
			}
		}
	}
	if again := read(); !reflect.DeepEqual(again, list) {
		t.Fatalf("ledger read changed the list: %+v -> %+v", list, again)
	}
	// A later revision that renames a step it has not started keeps the numbering
	// and the ledger read returns the new name.
	if _, err = engine.SubmitStepGraph(t.Context(), "g", "person", "agent", "conversation", 1, []goal.Step{step("stay", "숙소 정하기"), step("move", "이동 준비", "stay"), step("pack", "짐 싸기")}); err != nil {
		t.Fatal(err)
	}
	if list = read(); list[0].Label != firstLabel(list[0]) || list[1].Number != 2 || !strings.Contains(stepNames(list[0]), "숙소 정하기") {
		t.Fatalf("renamed step list = %+v", list)
	}
}

func randomSteps(r *rand.Rand, n int) []Step {
	steps := make([]Step, n)
	for i := range steps {
		steps[i] = Step{ID: fmt.Sprintf("step-%02d", i), Name: fmt.Sprintf("단계 %d", i+1), State: StepWaiting}
		for j := range i {
			if r.IntN(4) == 0 {
				steps[i].DependsOn = append(steps[i].DependsOn, steps[j].ID)
			}
		}
	}
	return steps
}

func randomRevisions(r *rand.Rand) []Revision {
	steps := randomSteps(r, 1+r.IntN(8))
	revisions := []Revision{{Steps: cloneSteps(steps)}}
	nextID := len(steps)
	for range 1 + r.IntN(3) {
		current := cloneSteps(steps)
		for i := range current {
			if current[i].State == StepWaiting && r.IntN(3) == 0 {
				current[i].State = StepDone
			}
		}
		for range r.IntN(3) {
			step := Step{ID: fmt.Sprintf("step-%02d", nextID), Name: fmt.Sprintf("추가 %d", nextID), State: StepWaiting}
			nextID++
			for j := range current {
				if r.IntN(3) == 0 {
					step.DependsOn = append(step.DependsOn, current[j].ID)
				}
			}
			current = append(current, step)
		}
		steps = current
		revisions = append(revisions, Revision{Steps: cloneSteps(current)})
	}
	return revisions
}

func cloneSteps(steps []Step) []Step {
	out := make([]Step, len(steps))
	for i, step := range steps {
		step.DependsOn = append([]string(nil), step.DependsOn...)
		out[i] = step
	}
	return out
}

// depths is an independent longest-chain computation over the generator's
// order, where every blocker has a lower index.
func depths(steps []Step) map[string]int {
	index := map[string]int{}
	for i, step := range steps {
		index[step.ID] = i
	}
	out := map[string]int{}
	for i, step := range steps {
		value := 1
		for _, blocker := range step.DependsOn {
			if at, ok := index[blocker]; ok && at < i && out[blocker]+1 > value {
				value = out[blocker] + 1
			}
		}
		out[step.ID] = value
	}
	return out
}

func depthCount(steps []Step) int {
	max := 0
	for _, depth := range depths(steps) {
		if depth > max {
			max = depth
		}
	}
	return max
}

// firstLabel states the design's label rule independently of the projection:
// the first step by start and then by id, plus the number of the others.
func firstLabel(entry Stage) string {
	first := firstStep(entry.Steps)
	if len(entry.Steps) == 1 {
		return first.Name
	}
	return fmt.Sprintf(design.Text(design.Korean, "stage.more"), first.Name, len(entry.Steps)-1)
}

func firstStep(steps []StageStep) StageStep {
	out := steps[0]
	for _, step := range steps[1:] {
		if step.StartedAt.Before(out.StartedAt) || step.StartedAt.Equal(out.StartedAt) && step.ID < out.ID {
			out = step
		}
	}
	return out
}

func stepNames(entry Stage) string {
	out := make([]string, 0, len(entry.Steps))
	for _, step := range entry.Steps {
		out = append(out, step.Name)
	}
	return strings.Join(out, ",")
}

func stepIDs(steps []StageStep) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		out = append(out, step.ID)
	}
	sort.Strings(out)
	return out
}

func numberedStages(list []Stage) map[int]Stage {
	out := map[int]Stage{}
	for _, entry := range list {
		if entry.Kind == KindStage {
			out[entry.Number] = entry
		}
	}
	return out
}

func firstOpenStage(list []Stage) (map[string]bool, bool) {
	for _, entry := range list {
		if entry.Kind != KindStage || entry.State == StepDone {
			continue
		}
		members := map[string]bool{}
		for _, step := range entry.Steps {
			if step.State != StepDone && step.State != StepSuperseded {
				members[step.ID] = true
			}
		}
		return members, len(members) > 0
	}
	return nil, false
}

type stageDefinitions struct{ repo *records.Store }

func (s stageDefinitions) Read(ctx context.Context, id string) (goal.DefinitionRecord, string, error) {
	r, rev, err := s.repo.Read(ctx, id)
	if errors.Is(err, records.ErrNotFound) {
		err = goal.ErrDefinitionMissing
	}
	return goal.DefinitionRecord{ID: r.ID, Path: r.Path, Data: r.Data}, rev, err
}
func (s stageDefinitions) Commit(ctx context.Context, c goal.DefinitionChange, person, agent string) (string, error) {
	return s.repo.Commit(ctx, records.Change{ID: c.ID, Base: c.Base, Path: c.Path, Data: c.Data}, records.Actor{Person: person, Agent: agent})
}
