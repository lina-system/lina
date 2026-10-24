// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

func TestInitialGoalStagesDoNotScaleWithUnrelatedHistory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	g := goal.Goal{ID: "goal", Person: "person", Agent: "agent", State: goal.GoalRunning, StepGraph: &goal.StepGraph{GoalID: "goal", Revision: 1, Steps: []goal.Step{{ID: "step", Name: "Check the result"}}}, Tasks: []goal.Task{{ID: "step", Stage: goal.TaskBuilding, Session: "worker"}}}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var stages []Stage
	read := func() {
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var err error
			stages, err = GoalStages(t.Context(), tx, g.ID, g.Person, design.English)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := testing.AllocsPerRun(3, read)
	want := stages
	// Other goals' retained revisions must not add work to the first revision
	// of this goal, whose complete graph and latest states are in its record.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		for i := range 82 {
			body, err := json.Marshal(map[string]any{"changes": []any{map[string]any{"kind": "goal", "id": fmt.Sprintf("other-%d", i), "body": g}}})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO work_events(id,kind,at,body) VALUES(?,'goal.step_graph',1,?)", fmt.Sprintf("history-%d", i), body); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after := testing.AllocsPerRun(3, read)
	if !reflect.DeepEqual(stages, want) {
		t.Fatal("unrelated history changed the initial graph")
	}
	t.Logf("initial stages: %.0f allocations before unrelated history, %.0f after", before, after)
	if after > 2*before {
		t.Fatalf("initial graph read scales with unrelated history: %.0f -> %.0f allocations", before, after)
	}
}
