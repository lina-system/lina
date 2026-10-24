// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/results"
)

func TestStartLineNamesTheHandover(t *testing.T) {
	g := oneGoal()
	g.Assumptions = []string{"the trip is next week"}
	g.Asks = []string{"confirm the hotel payment"}
	line, err := StartLine(g)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"Starting:", g.Summary, "Done means", g.Done, "Assumptions:", "the trip is next week", "I will ask:", "confirm the hotel payment"} {
		if !strings.Contains(line, part) {
			t.Fatalf("start line missing %q: %s", part, line)
		}
	}
	if strings.Contains(line, "\n") || strings.Contains(line, g.ID) || strings.Contains(line, "acceptance") {
		t.Fatalf("start line leaked: %s", line)
	}
}

func TestStartLineRejectsTechnicalProse(t *testing.T) {
	g := oneGoal()
	g.Summary = "Fix issue #500"
	if _, err := StartLine(g); !errors.Is(err, results.ErrPersonLine) {
		t.Fatalf("err = %v", err)
	}
}

func TestCloseLineGivesOneClauseAndSaysWhatIsMissing(t *testing.T) {
	g := oneGoal()
	g.Criteria = goalCriteria(g)
	g.State = GoalDone
	g.PersonTasks = []string{"pack the bag"}
	line, err := CloseLine(g)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "done.") {
		t.Fatal(line)
	}
	for _, c := range g.Criteria {
		if !strings.Contains(line, c.Statement) {
			t.Fatalf("missing clause %q: %s", c.Statement, line)
		}
	}
	if !strings.Contains(line, "pack the bag") {
		t.Fatal(line)
	}
	g.State = GoalBlocked
	g.BlockedReason = "the hotel reply never came"
	g.Checks = nil
	g.PersonTasks = nil
	line, err = CloseLine(g)
	if err != nil || !strings.HasPrefix(line, "blocked.") || !strings.Contains(line, "the hotel reply never came") || !strings.Contains(line, "Missing:") {
		t.Fatalf("blocked: %s %v", line, err)
	}
	g.State = GoalStopped
	g.BlockedReason = ""
	line, err = CloseLine(g)
	if err != nil || !strings.HasPrefix(line, "stopped.") || !strings.Contains(line, "Missing:") {
		t.Fatalf("stopped: %s %v", line, err)
	}
}

func TestTaskLandingChangesTheRunningRow(t *testing.T) {
	task := readyTask("write")
	task.Name = "Write the note"
	summary, state := RunningRow(task)
	if summary != "Write the note" || state != "working" {
		t.Fatalf("before landing: %s %s", summary, state)
	}
	task.Stage = TaskDone
	summary, state = RunningRow(task)
	if summary != "Write the note" || state != "done" {
		t.Fatalf("after landing: %s %s", summary, state)
	}
	task.Name = "issue #3"
	summary, _ = RunningRow(task)
	if summary == "issue #3" || summary == "" {
		t.Fatalf("technical name stayed on the row: %q", summary)
	}
}
