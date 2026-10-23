// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"testing"
	"time"
)

func TestWorkPanelStandaloneGoalAndGroupedProjects(t *testing.T) {
	units := []Unit{
		{ID: "standalone", Kind: "goal", Name: "Trip", Active: true, State: "in_progress", StartedAt: time.Unix(1, 0), Now: "Book hotel", Stages: []Stage{{Label: "Book hotel", State: StepWorking}}},
		{ID: "group", Kind: "goal", Active: true},
		{ID: "child", Kind: "project", Parent: "group", Active: true},
	}
	entries := Compute(units)
	for _, entry := range entries {
		switch entry.Ref {
		case "standalone":
			if entry.Line.Kind != "project" || entry.Line.Now != "Book hotel" || len(entry.Line.Stages) != 1 || !entry.Line.StartedAt.Equal(time.Unix(1, 0)) {
				t.Fatalf("executable goal has no live card: %+v", entry)
			}
		case "group":
			if entry.Line.Kind != "goal" {
				t.Fatalf("group became duplicate card: %+v", entry)
			}
		case "child":
			if entry.Line.Kind != "project" || entry.Line.Depth != 1 {
				t.Fatalf("child lost hierarchy: %+v", entry)
			}
		}
	}
	if units[0].Kind != "goal" {
		t.Fatal("projection mutated input")
	}
}
