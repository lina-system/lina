// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestProgressLinePublishesStageEntries checks that a goal line reaches the
// wire with its Now, start instant and stage entries, and that a recomputation
// of unchanged work publishes no frame.
func TestProgressLinePublishesStageEntries(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	g := goal.Goal{
		ID: "line-goal", Person: r.person, Agent: r.agent, Conversation: r.conversation,
		State: goal.GoalRunning, CreatedAt: 1_700_000_000,
		StepGraph: &goal.StepGraph{GoalID: "line-goal", Revision: 1, Steps: []goal.Step{
			{ID: "draft", Name: "Draft workshop", Done: "The draft is ready", Repository: "fixture", Base: "dev", Area: "draft.md", Worker: "stub", Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, DependsOn: []string{}},
			{ID: "check", Name: "Check workshop", Done: "The draft is checked", Repository: "fixture", Base: "dev", Area: "check.md", Worker: "stub", Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, DependsOn: []string{"draft"}},
		}},
		Tasks: []goal.Task{
			{ID: "draft", Name: "Draft workshop", Stage: goal.TaskBuilding, Repository: "fixture", Base: "dev", Area: "draft.md", Worker: "stub"},
			{ID: "check", Name: "Check workshop", Stage: goal.TaskReady, Repository: "fixture", Base: "dev", Area: "check.md", Worker: "stub", DependsOn: []string{"draft"}},
		},
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.publishOwnerViews(t.Context()); err != nil {
		t.Fatal(err)
	}
	start, sub, err := r.topics.Subscribe("progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(start.Snapshot.Items) != 1 {
		t.Fatalf("progress snapshot items = %d", len(start.Snapshot.Items))
	}
	var update protocol.ProgressUpdate
	if err = json.Unmarshal(start.Snapshot.Items[0].Data, &update); err != nil {
		t.Fatal(err)
	}
	if update.Rows == nil || len(*update.Rows) != 1 || (*update.Rows)[0].Ref != g.ID {
		t.Fatalf("goal line missing: %+v", update.Rows)
	}
	line := (*update.Rows)[0].Line
	if line.StartedAt == nil || *line.StartedAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("started_at = %v, want the hand-over instant", line.StartedAt)
	}
	if line.Now == nil || *line.Now != "Draft workshop" {
		t.Fatalf("now = %v, want the working step", line.Now)
	}
	if line.Stages == nil || len(*line.Stages) != 3 {
		t.Fatalf("stages = %+v, want two numbered entries and 확인", line.Stages)
	}
	first := (*line.Stages)[0]
	if first.Number == nil || *first.Number != "1" || first.Label != "Draft workshop" || first.State != string(progress.StepWorking) {
		t.Fatalf("first stage = %+v", first)
	}
	if first.Steps == nil || len(*first.Steps) != 1 || (*first.Steps)[0].Name != "Draft workshop" || (*first.Steps)[0].StartedAt != nil {
		t.Fatalf("first stage steps = %+v", first.Steps)
	}
	if last := (*line.Stages)[2]; last.Kind != string(progress.KindChecking) || last.Number != nil {
		t.Fatalf("checking entry = %+v", last)
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{`"id"`, `"ref"`} {
		if strings.Contains(string(raw), hidden) {
			t.Fatalf("line carries identity: %s", raw)
		}
	}
	// An unchanged recomputation publishes no frame.
	before := start.Snapshot.Cursor
	if err = r.publishOwnerViews(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, sub2, err := r.topics.Subscribe("progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub2.Close()
	if !reflect.DeepEqual(before, again.Snapshot.Cursor) {
		t.Fatal("unchanged recomputation advanced the progress topic")
	}
}

// TestProgressLineWithMissingDefinitionKeepsTheLine checks that a goal whose
// record was removed still shows its line with an unknown name instead of
// failing the whole progress view.
func TestProgressLineWithMissingDefinitionKeepsTheLine(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	g := goal.Goal{
		ID: "orphan-goal", Person: r.person, Agent: r.agent, Conversation: r.conversation,
		RecordID: "goal:orphan-goal", RecordRevision: "missing", State: goal.GoalRunning,
		CreatedAt: 1_700_000_000,
		Tasks: []goal.Task{{
			ID: "orphan-step", Name: "Collect dates", Stage: goal.TaskBuilding,
			Repository: "fixture", Base: "dev", Area: "dates.md", Worker: "stub",
			Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1},
		}},
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatalf("a missing definition failed the progress view: %v", err)
	}
	if p.Rows == nil || len(*p.Rows) != 1 || (*p.Rows)[0].Ref != g.ID {
		t.Fatalf("goal line missing: %+v", p.Rows)
	}
	if name := (*p.Rows)[0].Line.Name; name != "Untitled" {
		t.Fatalf("name = %q, want the untitled word", name)
	}
}

// TestProgressLineKeepsReadableGoalTitles checks that a missing or unreadable
// definition leaves only that goal unnamed. After a restart, an older
// unreadable record must not replace the sentences of goals that can still be
// read.
func TestProgressLineKeepsReadableGoalTitles(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	agent := workAgentFixture(t, r)
	cfg := &WorkConfig{Driver: func(*goal.Engine) (goal.Driver, error) { return parkedWorkDriver{}, nil }}
	if err := r.startWork(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	live := goal.Goal{
		ID: "live-goal", Person: r.person, Agent: agent, Conversation: r.conversation,
		Summary: "Book the workshop", Done: "The workshop is booked", NoServiceReason: "plan",
		Scope:    goal.GoalScope{Repositories: []string{"repo"}, Includes: "plan"},
		Criteria: []goal.Criterion{{ID: "booked", Statement: "The workshop is booked", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "repo"}}},
	}
	if err := r.workEngine.engine.Admit(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	// A restarted engine has an empty definition cache and must read the sentence
	// from the canonical record. The ledger itself does not store that prose.
	if err := r.workEngine.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.startWork(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	names := progressNames(t, r)
	if names["live-goal"] != live.Summary {
		t.Fatalf("restarted name = %q, want %q (%v)", names["live-goal"], live.Summary, names)
	}
	if err := r.workEngine.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	invalidID := "goal:ccc-invalid"
	rev, err := r.records.Commit(t.Context(), records.Change{
		ID: invalidID, Path: "projects/ccc-invalid.md",
		Data: []byte(fmt.Sprintf("---\nid: %q\nkind: goal\nstate: active\nperson_id: %q\nagent_id: %q\ngoal: not-json\n---\n\n# Broken\n\n", invalidID, r.person, agent)),
	}, records.Actor{Person: r.person, Agent: agent})
	if err != nil {
		t.Fatal(err)
	}
	step := goal.Task{
		ID: "step", Name: "Collect dates", Stage: goal.TaskBuilding,
		Repository: "fixture", Base: "dev", Area: "dates.md", Worker: "stub",
		Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1},
	}
	insertGoal(t, r, goal.Goal{
		ID: "aaa-finished", Person: r.person, Agent: agent, Conversation: r.conversation,
		RecordID: "goal:aaa-finished", RecordRevision: "missing", State: goal.GoalDone, CreatedAt: 1,
	})
	insertGoal(t, r, goal.Goal{
		ID: "bbb-missing", Person: r.person, Agent: agent, Conversation: r.conversation,
		RecordID: "goal:bbb-missing", RecordRevision: "missing", State: goal.GoalRunning, CreatedAt: 1,
		Tasks: []goal.Task{step},
	})
	insertGoal(t, r, goal.Goal{
		ID: "ccc-invalid", Person: r.person, Agent: agent, Conversation: r.conversation,
		RecordID: invalidID, RecordRevision: rev, State: goal.GoalRunning, CreatedAt: 1,
		Tasks: []goal.Task{step},
	})
	// Same database, empty cache: the condition after a restart, before reconcile
	// has fenced the unreadable records. Those records sort ahead of the live goal.
	fresh, err := goal.NewEngine(goal.Config{DB: r.db, Records: workDefinitions{r.records}, Epoch: r.epoch, Driver: parkedWorkDriver{}})
	if err != nil {
		t.Fatal(err)
	}
	r.workEngine.engine = fresh
	names = progressNames(t, r)
	if _, ok := names["aaa-finished"]; ok {
		t.Fatalf("finished goal stayed on the line: %v", names)
	}
	if names["bbb-missing"] != "Untitled" || names["ccc-invalid"] != "Untitled" {
		t.Fatalf("unreadable goals = %q %q, want untitled", names["bbb-missing"], names["ccc-invalid"])
	}
	if names["live-goal"] != live.Summary {
		t.Fatalf("readable goal name = %q, want %q (%v)", names["live-goal"], live.Summary, names)
	}
}

func insertGoal(t *testing.T, r *conversationRuntime, g goal.Goal) {
	t.Helper()
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func progressNames(t *testing.T, r *conversationRuntime) map[string]string {
	t.Helper()
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatalf("progress view: %v", err)
	}
	if p.Rows == nil {
		t.Fatal("progress view has no rows")
	}
	names := map[string]string{}
	for _, row := range *p.Rows {
		names[row.Ref] = row.Line.Name
	}
	return names
}
