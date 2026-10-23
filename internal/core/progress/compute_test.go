// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComputeUnknownHierarchyAndSeenCompletion(t *testing.T) {
	yes := true
	units := []Unit{
		{ID: "private-goal", Kind: "goal", Name: "Ship the workshop"},
		{ID: "private-project", Parent: "private-goal", Kind: "project", Name: "Prepare materials", Active: true, Waiting: 1, Updated: 9},
		{ID: "unknown-project", Kind: "project", Name: "Explore the topic", Active: true},
	}
	got := Compute(units)
	if len(got) != 3 || got[0].Ref != "private-goal" || got[1].Ref != "private-project" {
		t.Fatalf("hierarchy/order: %+v", got)
	}
	for _, entry := range got {
		v := entry.Line
		if v.Stage != "unknown" || v.Percent != nil || v.JustFinished != "unknown" || v.Next != "unknown" {
			t.Fatalf("unobserved: %+v", v)
		}
	}
	raw, _ := json.Marshal(got[1].Line)
	for _, hidden := range []string{"private-", "pull_request", "\"id\"", "\"ref\""} {
		if strings.Contains(string(raw), hidden) {
			t.Fatalf("identity in line: %s", raw)
		}
	}
	units[1].Done = &yes
	got = Compute(units)
	if len(got) != 3 || got[1].Line.Stage != "unknown" {
		t.Fatalf("unseen result lost or plan invented: %+v", got)
	}
	units[1].Seen = true
	got = Compute(units)
	if len(got) != 1 || got[0].Ref != "unknown-project" {
		t.Fatalf("seen completion retained: %+v", got)
	}
}
func TestComputeOrderUsesWaitingThenLatestMovement(t *testing.T) {
	got := Compute([]Unit{{ID: "a", Name: "Earlier", Active: true, Updated: 1}, {ID: "b", Name: "Latest", Active: true, Updated: 2}, {ID: "c", Name: "Needs a reply", Waiting: 1}})
	if len(got) != 3 || got[0].Ref != "c" || got[1].Ref != "b" || got[2].Ref != "a" {
		t.Fatalf("order: %+v", got)
	}
}
func TestGoalDoesNotBecomeDoneWithoutItsOwnVerdict(t *testing.T) {
	yes := true
	rows := Compute([]Unit{{ID: "goal", Kind: "goal", Name: "Goal", Waiting: 1}, {ID: "project", Parent: "goal", Kind: "project", Name: "Project", Done: &yes, Seen: true}})
	if len(rows) != 1 || rows[0].Line.Stage != "unknown" || rows[0].Line.Percent != nil {
		t.Fatalf("unobserved goal plan became known: %+v", rows)
	}
}

// TestComputeGoalCarriesCurrentStageAndEntries checks that a goal line's stage
// word is its current stage entry and that the entries stay on the line. A
// running goal is visible on its own, without a child row.
func TestComputeGoalCarriesCurrentStageAndEntries(t *testing.T) {
	stages := []Stage{
		{Number: 1, Kind: KindStage, Label: "숙소 고르기", State: StepDone, Steps: []StageStep{{ID: "private-step", Name: "숙소 고르기", State: StepDone}}},
		{Number: 2, Kind: KindStage, Label: "이동 준비", State: StepWorking, Steps: []StageStep{{ID: "private-step-2", Name: "이동 준비", State: StepWorking}}},
		{Kind: KindChecking, Label: "확인", State: StepNext},
	}
	rows := Compute([]Unit{{ID: "private-goal", Kind: "goal", Name: "Ship the workshop", Active: true, Stages: stages}})
	if len(rows) != 1 {
		t.Fatalf("running goal without children disappeared: %+v", rows)
	}
	line := rows[0].Line
	if line.Stage != "이동 준비" || len(line.Stages) != 3 || line.Stages[2].Kind != KindChecking {
		t.Fatalf("line = %+v", line)
	}
	// The wire line carries no step identity.
	raw, _ := json.Marshal(line)
	if strings.Contains(string(raw), "private-step") {
		t.Fatalf("identity in line: %s", raw)
	}
	// A landed stage moves the current entry past it.
	stages[1].State = StepDone
	rows = Compute([]Unit{{ID: "private-goal", Kind: "goal", Name: "Ship the workshop", Active: true, Stages: stages}})
	if len(rows) != 1 || rows[0].Line.Stage != "확인" {
		t.Fatalf("current stage after landing = %+v", rows)
	}
}
