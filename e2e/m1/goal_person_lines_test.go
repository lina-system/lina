// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// TestM2GoalPersonLines is the headless scenario for one start line and one
// closing summary. The script lives in e2e/m2/goal-person-lines.json. Stub
// workers and plugins are the installed code-goal fixtures; the test waits on
// ledger facts, not on a clock.
func TestM2GoalPersonLines(t *testing.T) {
	t.Parallel()
	installedCodeGoal(t, "person-lines")
}

type personLines struct {
	Outcome, Done, Statement string
	Assumptions, Asks        []string
	PersonTasks              []string `json:"person_tasks"`
}

func personLineScript(t *testing.T) personLines {
	t.Helper()
	raw, err := os.ReadFile("../m2/goal-person-lines.json")
	if err != nil {
		t.Fatal(err)
	}
	var script personLines
	if err = json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	if script.Outcome == "" || script.Done == "" || script.Statement == "" || len(script.Assumptions) == 0 || len(script.Asks) == 0 || len(script.PersonTasks) == 0 {
		t.Fatalf("incomplete person-line script: %+v", script)
	}
	return script
}

func assertPersonLines(t *testing.T, db *canon.Store, g goal.Goal) {
	t.Helper()
	script := personLineScript(t)
	var start, summary string
	var notices int
	var stages []string
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT scaffold_kind, body FROM conv_event WHERE scaffold_kind IN ('goal_start','goal_result','worker_result','background_notice')")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, body string
			if err = rows.Scan(&kind, &body); err != nil {
				return err
			}
			switch kind {
			case "goal_start":
				if start != "" {
					t.Fatalf("more than one start line: %s", body)
				}
				start = body
			case "goal_result":
				if summary != "" {
					t.Fatalf("more than one summary: %s", body)
				}
				summary = body
			default:
				notices++
			}
		}
		if err = rows.Err(); err != nil {
			return err
		}
		rows, err = tx.QueryContext(t.Context(), "SELECT body FROM work_events")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body string
			if err = rows.Scan(&body); err != nil {
				return err
			}
			for _, stage := range []string{`"stage":"ready"`, `"stage":"building"`, `"stage":"done"`} {
				if strings.Contains(body, stage) {
					stages = append(stages, stage)
				}
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatalf("a task landing sent %d notices", notices)
	}
	for _, part := range []string{"Starting:", script.Outcome, "Done means", script.Done, "Assumptions:", script.Assumptions[0], "I will ask:", script.Asks[0]} {
		if !strings.Contains(start, part) {
			t.Fatalf("start line missing %q: %s", part, start)
		}
	}
	if !strings.HasPrefix(summary, "done.") || !strings.Contains(summary, script.Statement) || !strings.Contains(summary, script.PersonTasks[0]) {
		t.Fatalf("summary: %s", summary)
	}
	for _, hidden := range []string{"#", "issue", "pull", "branch", "acceptance", "result.txt", "fixture/project", g.ID} {
		if strings.Contains(start, hidden) || strings.Contains(summary, hidden) {
			t.Fatalf("line names %q\nstart: %s\nsummary: %s", hidden, start, summary)
		}
	}
	seen := map[string]bool{}
	for _, stage := range stages {
		seen[stage] = true
	}
	if !seen[`"stage":"done"`] || !(seen[`"stage":"ready"`] || seen[`"stage":"building"`]) {
		t.Fatalf("task row did not change: %v", stages)
	}
	if len(g.Tasks) == 0 || g.Tasks[0].Stage != goal.TaskDone {
		t.Fatalf("landed task: %+v", g.Tasks)
	}
	_, before := goal.RunningRow(goal.Task{Name: g.Tasks[0].Name, Stage: goal.TaskReady})
	_, after := goal.RunningRow(g.Tasks[0])
	if before == after {
		t.Fatalf("running row stayed %s", after)
	}
}
