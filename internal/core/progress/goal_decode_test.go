// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

func TestProgressGoalDecodeKeepsDisplayWithoutExecutionBriefs(t *testing.T) {
	g := goal.Goal{ID: "goal", Person: "person", Agent: "agent", Turn: "turn", CreatedAt: 100, State: goal.GoalRunning, AttentionID: "attention", StepGraph: &goal.StepGraph{GoalID: "goal", Revision: 3}}
	for i := range 82 {
		id := fmt.Sprintf("step-%02d", i)
		brief := &goal.BackgroundBrief{Goal: "Run the check", Scope: "fixture", NotTouch: "other work", Done: "Verified", Result: "notes"}
		for j := range 20 {
			brief.Checks = append(brief.Checks, goal.BackgroundCheck{Name: fmt.Sprintf("check-%d", j), Kind: "claimed"})
		}
		var after []string
		if i > 0 {
			after = []string{"step-00"}
		}
		g.StepGraph.Steps = append(g.StepGraph.Steps, goal.Step{ID: id, Name: "Check " + id, DependsOn: after, Background: brief})
		g.Tasks = append(g.Tasks, goal.Task{ID: id, Stage: goal.TaskBuilding, Session: "session-" + id, Background: brief})
	}
	g.Tasks[0].Stage = goal.TaskDone
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeProgressGoal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != g.ID || decoded.Person != g.Person || decoded.Agent != g.Agent || decoded.Turn != g.Turn || decoded.CreatedAt != g.CreatedAt || decoded.State != g.State || decoded.AttentionID != g.AttentionID || decoded.StepGraph.Revision != g.StepGraph.Revision {
		t.Fatal("progress lost goal identity, scope or state")
	}
	for _, language := range []design.Language{design.English, design.Korean} {
		want, err := stagesForGoal(g, nil, nil, nil, language)
		if err != nil {
			t.Fatal(err)
		}
		got, err := stagesForGoal(decoded, nil, nil, nil, language)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || goalNow(decoded, got, language) != goalNow(g, want, language) || goalState(decoded, got) != goalState(g, want) || goalWaiting(decoded) != goalWaiting(g) || !reflect.DeepEqual(goalPercent(decoded), goalPercent(g)) {
			t.Fatalf("%s display differs from the execution record", language)
		}
	}
	full := testing.AllocsPerRun(3, func() {
		var current goal.Goal
		if err := json.Unmarshal(raw, &current); err != nil {
			t.Fatal(err)
		}
	})
	projected := testing.AllocsPerRun(3, func() {
		if _, err := decodeProgressGoal(raw); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("full goal %.0f allocations, progress fields %.0f", full, projected)
	if projected >= full/2 {
		t.Fatalf("display decoding still materializes execution briefs: %.0f allocations; full record %.0f", projected, full)
	}
}

func TestProgressGoalDecodeKeepsAbsentGraphAndRejectsMalformedDisplay(t *testing.T) {
	for _, raw := range []string{`{}`, `{"tasks":null,"step_graph":null}`, `{"tasks":[],"step_graph":{"revision":1,"steps":[]}}`} {
		var full goal.Goal
		if err := json.Unmarshal([]byte(raw), &full); err != nil {
			t.Fatal(err)
		}
		got, err := decodeProgressGoal([]byte(raw))
		if err != nil || !reflect.DeepEqual(got, full) {
			t.Fatalf("absent or empty graph changed: %s: %+v, %v", raw, got, err)
		}
	}
	for _, raw := range []string{`{`, `[]`, `{"person":1}`, `{"tasks":[{"stage":1}]}`, `{"tasks":[{"session":1}]}`, `{"step_graph":{"revision":"bad"}}`, `{"step_graph":{"steps":[{"depends_on":1}]}}`} {
		if _, err := decodeProgressGoal([]byte(raw)); err == nil {
			t.Fatalf("malformed display accepted: %s", raw)
		}
	}
}
