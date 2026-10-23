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
