// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"fmt"
	"testing"
)

// TestCountFollowsLinesNotRows checks the running count of three 일 holding
// twelve tasks and one background run outside any 일: four lines by state, and
// twelve task rows of those lines, and the run outside any 일 as a line of its
// own: four lines by state while the running list still shows thirteen rows.
func TestCountFollowsLinesNotRows(t *testing.T) {
	units := []Unit{}
	tasks := 0
	for i := range 3 {
		id := fmt.Sprintf("day-%d", i)
		steps := []StageStep{}
		stages := []Stage{{Number: 1, Kind: KindStage, Label: fmt.Sprintf("Stage %d", i), State: StepWorking}}
		for j := range 4 {
			step := StageStep{ID: fmt.Sprintf("step-%d-%d", i, j), Name: fmt.Sprintf("step-%d-%d", i, j), State: StepWorking}
			if i == 2 && j == 3 {
				step.State = StepNeedsInput
			}
			steps = append(steps, step)
			// The step's card carries the step id and no parent of its own.
			units = append(units, Unit{ID: step.ID, Kind: "project", Name: step.Name, State: "in_progress", Active: true})
			tasks++
		}
		stages[0].Steps = steps
		state, waiting := "in_progress", 0
		if i == 2 {
			state, waiting = "needs_input", 1
		}
		units = append(units, Unit{ID: id, Kind: "project", Name: id, State: state, Active: true, Stages: stages, Waiting: waiting})
	}
	// A dispatched step's card carries the 일's id rather than the step id.
	units = append(units, Unit{ID: "dispatch-hash", Parent: "day-0", Kind: "project", Name: "Collect dates", State: "in_progress", Active: true})
	units = append(units, Unit{ID: "run:survey", Kind: "project", Name: "Read competitor notes", State: "in_progress", Active: true})
	entries := Compute(units)
	lines, rows := 0, 0
	for _, entry := range entries {
		if entry.Line.Depth == 0 {
			lines++
			continue
		}
		rows++
	}
	if lines != 4 || rows != tasks+1 {
		t.Fatalf("projection has %d lines and %d rows, want 4 and %d: %+v", lines, rows, tasks+1, entries)
	}
	if got := CountLines(entries); got != (Count{Running: 3, Waiting: 1}) {
		t.Fatalf("count = %+v, want three running lines and one waiting for the person", got)
	}
}
