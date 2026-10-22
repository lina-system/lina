// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
)

func TestWorkUnitsDropsSeenCompletionAndRereadsGeneration(t *testing.T) {
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
	card := work.Card{Task: work.Task{ID: "private-task", AgentID: "agent", PersonID: "person", Title: "Finished materials"}, State: "done", Verdict: "verified", Run: &work.Run{Generation: 1}}
	save := func() {
		t.Helper()
		raw, _ := json.Marshal(card)
		if err = db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT OR REPLACE INTO work_cards(task_id,record) VALUES(?,?)", card.Task.ID, raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []Entry {
		t.Helper()
		var units []Unit
		if err = db.Read(t.Context(), func(tx *canon.Tx) error {
			var err error
			units, err = WorkUnits(t.Context(), tx, "person", map[string]string{"agent": "LINA"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return Compute(units)
	}
	save()
	if len(read()) != 1 {
		t.Fatal("unseen completed work missing")
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO running_seen(source,item_id,person) VALUES('work','private-task:1','person')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("seen completion retained: %+v", got)
	}
	card.State = "unknown"
	card.Verdict = ""
	card.Run.Generation = 2
	save()
	if got := read(); len(got) != 1 || got[0].Line.Stage != "unknown" {
		t.Fatalf("new execution hidden or falsely done: %+v", got)
	}
	for _, state := range []string{"failed", "stopped", "ready_for_review"} {
		card.State = state
		card.Run.Generation = 1
		save()
		if got := read(); len(got) != 0 {
			t.Fatalf("acknowledged %s result retained: %+v", state, got)
		}
		card.Run.Generation = 2
		save()
		if got := read(); len(got) != 1 || got[0].Line.Stage != "unknown" {
			t.Fatalf("new %s result missing or plan invented: %+v", state, got)
		}
	}
	card.State = "working"
	card.Run.Generation = 1
	save()
	if got := read(); len(got) != 1 {
		t.Fatalf("seen acknowledgement hid work needing changes: %+v", got)
	}

}

func TestReadyForReviewIsNotACompletionVerdict(t *testing.T) {
	for _, verdict := range []string{"", "verified"} {
		units := UnplannedWork(map[string]work.Card{
			"task": {Task: work.Task{ID: "task", Title: "Prepare materials", RequiresApply: true}, State: "ready_for_review", Verdict: verdict, Run: &work.Run{Generation: 1}},
		})
		if len(units) != 1 || units[0].Done != nil && *units[0].Done {
			t.Fatalf("ready result became successful completion: %+v", units)
		}
		rows := Compute(units)
		if len(rows) != 1 || rows[0].Line.Stage != "unknown" || rows[0].Line.Percent != nil {
			t.Fatalf("unseen ready result missing or plan invented: %+v", rows)
		}
		units[0].Seen = true
		if rows = Compute(units); len(rows) != 0 {
			t.Fatalf("seen ready result retained (verdict %q): %+v", verdict, rows)
		}
	}
}

func TestUnplannedWorkDoesNotInventPlanValues(t *testing.T) {
	cards := map[string]work.Card{
		"goal": {Task: work.Task{ID: "goal", Title: "Goal"}},
		"task": {Task: work.Task{ID: "task", GoalID: "goal", Title: "Task"}, State: "done", Verdict: "verified", Run: &work.Run{Generation: 1}, Receipt: &work.Receipt{Outcome: "ready_for_review"}, TurnStatus: "completed"},
	}
	rows := Compute(UnplannedWork(cards))
	if len(rows) != 2 {
		t.Fatalf("missing work: %+v", rows)
	}
	for _, row := range rows {
		v := row.Line
		if v.Stage != "unknown" || v.Percent != nil || v.Next != "unknown" || v.JustFinished != "unknown" {
			t.Fatalf("task verdict substituted for plan observation: %+v", v)
		}
	}
}
