// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type runningCountScenario struct {
	Goals []struct {
		ID    string
		Steps []string
	}
	Run struct {
		Goal string
	}
	Lines, Tasks, Rows, Width, Height int
}

// TestM3RunningCountInstalledCore (m3/running-count.json) seeds three 일 that
// hold twelve tasks and one background run outside any 일. Installed Core
// reports four lines by state while the running list still shows thirteen rows,
// and the TUI status line prints Core's count rather than the number of rows.
func TestM3RunningCountInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("../m3/running-count.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario runningCountScenario
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.Lines == 0 || scenario.Tasks == 0 || scenario.Rows == 0 || scenario.Width == 0 || scenario.Height == 0 {
		t.Fatalf("incomplete scenario: %+v", scenario)
	}
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	core := start(true)
	h.stopCore(core)
	seedRunningCount(t, h, scenario)
	core = start(true)
	defer h.stopCore(core)

	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "running-count", []protocol.Capability{{Name: "progress", Version: "1.0"}, {Name: "running", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mode := "overview"
	if _, err = c.Progress(h.ctx, protocol.ProgressGet{View: &mode}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	p, ok := frame.Payload.(protocol.Progress)
	if err != nil || frame.Error != nil || !ok || p.Rows == nil {
		t.Fatalf("progress unavailable: %+v %v", frame, err)
	}
	if len(*p.Rows) != scenario.Lines+scenario.Tasks {
		t.Fatalf("progress rows = %d, want %d lines and %d task rows: %+v", len(*p.Rows), scenario.Lines, scenario.Tasks, *p.Rows)
	}
	steps := 0
	for _, row := range *p.Rows {
		if row.Line.Row != nil && *row.Line.Row {
			steps++
			if row.Line.Depth != "1" {
				t.Fatalf("step row %s walks outside its 일 at depth %s", row.Ref, row.Line.Depth)
			}
		}
	}
	if steps != scenario.Tasks {
		t.Fatalf("step rows = %d, want the fixture's %d tasks marked as rows", steps, scenario.Tasks)
	}
	if p.Counts == nil {
		t.Fatalf("overview carries no running count: %+v", p)
	}
	running, _ := p.Counts.Running.Int64()
	waiting, _ := p.Counts.Waiting.Int64()
	if int(running+waiting) != scenario.Lines {
		t.Fatalf("count = %d running, %d waiting; want %d lines by state", running, waiting, scenario.Lines)
	}
	if running == 0 || waiting == 0 {
		t.Fatalf("fixture lost its mixed states: %d running, %d waiting", running, waiting)
	}
	topic := "running"
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err = c.Receive(h.ctx)
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if err != nil || frame.Error != nil || !ok || len(snapshot.Items) != scenario.Rows {
		t.Fatalf("running list = %+v %v, want %d rows", frame, err, scenario.Rows)
	}

	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=en_US.UTF-8"}, false, true, scenario.Width, scenario.Height)
	want := fmt.Sprintf(design.Text(design.English, "work.count"), running, waiting)
	snap, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return s.AltScreen && !s.CursorHidden && strings.Contains(screenText(s), want)
	})
	if err != nil {
		t.Fatalf("status count %q missing: %v\n%s", want, err, screenText(snap))
	}
}

// seedRunningCount admits the fixture's 일 through the work engine while Core
// is stopped, so Core rebuilds both the records and the ledger at startup.
func seedRunningCount(t *testing.T, h *harness, scenario runningCountScenario) {
	t.Helper()
	db, close := installedAcceptanceStore(t, h)
	defer close()
	var person, agent string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT r.person_id,f.agent_id FROM agent_first_run f JOIN agent_revisions r ON r.agent_id=f.agent_id AND r.revision=1").Scan(&person, &agent)
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := records.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	engine, err := goal.NewEngine(goal.Config{DB: db, Records: countDefinitions{repo}, Epoch: 1, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	conversation := "main:" + agent
	at := time.Now().UTC().Add(-time.Hour)
	for _, day := range scenario.Goals {
		g := goal.Goal{ID: day.ID, Person: person, Agent: agent, Conversation: conversation, Summary: "Day " + day.ID, Done: "The day is planned", NoServiceReason: "plan",
			Scope:    goal.GoalScope{Repositories: []string{"fixture"}, Includes: day.ID},
			Criteria: []goal.Criterion{{ID: "outcome", Statement: "The day is planned", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "fixture"}}}}
		if err = engine.Admit(h.ctx, g); err != nil {
			t.Fatal(err)
		}
		steps := make([]goal.Step, 0, len(day.Steps))
		for i, name := range day.Steps {
			id := stepID(day.ID, i)
			steps = append(steps, goal.Step{ID: id, Name: name, Done: "Produce " + name, Repository: "fixture", Base: "dev", Area: name + ".md", Worker: "fixture", Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}})
		}
		if _, err = engine.SubmitStepGraph(h.ctx, day.ID, person, agent, conversation, 0, steps); err != nil {
			t.Fatal(err)
		}
		// The fixture's steps are executing: the record's own task states are
		// what the line reads, so mark each step as building before Core starts.
		if err = db.Write(h.ctx, func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(h.ctx, "SELECT body FROM work_records WHERE kind='goal' AND id=?", day.ID).Scan(&raw); err != nil {
				return err
			}
			var saved goal.Goal
			if err := json.Unmarshal(raw, &saved); err != nil {
				return err
			}
			for i := range saved.Tasks {
				saved.Tasks[i].Stage = goal.TaskBuilding
			}
			body, err := json.Marshal(saved)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(h.ctx, "UPDATE work_records SET body=? WHERE kind='goal' AND id=?", body, day.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The step cards are dispatched work: one card per step, carrying the step
	// id so the line and its rows stay one 일.
	if err = db.Write(h.ctx, func(tx *canon.Tx) error {
		for _, day := range scenario.Goals {
			for i, name := range day.Steps {
				id := stepID(day.ID, i)
				task := work.Task{ID: id, PersonID: person, AgentID: agent, ConversationID: conversation, Title: name, Done: "Produce " + name}
				run := work.Run{ID: id + ":run", TaskID: id, Generation: 1, Worker: "fixture", ThreadID: id, TurnID: id, NodeID: "local"}
				events := []work.Event{
					{Key: id + ":create", TaskID: id, Kind: "created", Task: &task, RecordedAt: at.Format(time.RFC3339Nano)},
					{Key: id + ":start", TaskID: id, Kind: "started", Run: &run, RecordedAt: at.Add(time.Minute).Format(time.RFC3339Nano)},
				}
				if _, err := work.Project(events); err != nil {
					return err
				}
				for _, event := range events {
					line, err := json.Marshal(event)
					if err != nil {
						return err
					}
					if _, err = tx.ExecContext(h.ctx, "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", id, event.Key, line); err != nil {
						return err
					}
				}
			}
		}
		run := runs.Record{ID: "notes", Origin: runs.Origin{Person: person, Agent: agent, Conversation: conversation}, State: runs.NeedsInput, Created: at.UnixNano(), Brief: runs.Brief{Goal: scenario.Run.Goal, Scope: "competitors", Done: "Notes are read", Result: "notes"}}
		body, err := json.Marshal(run)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(h.ctx, "INSERT INTO background_runs(id,record) VALUES(?,?)", run.ID, body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// stepID names a step's card. The id is opaque; the fixture holds the words.
func stepID(goalID string, index int) string { return fmt.Sprintf("%s-step-%d", goalID, index) }

// countDefinitions joins the records store to the goal engine's definition
// interface, which the installed Core does for itself.
type countDefinitions struct{ repo *records.Store }

func (d countDefinitions) Read(ctx context.Context, id string) (goal.DefinitionRecord, string, error) {
	r, rev, err := d.repo.Read(ctx, id)
	if errors.Is(err, records.ErrNotFound) {
		err = goal.ErrDefinitionMissing
	}
	return goal.DefinitionRecord{ID: r.ID, Path: r.Path, Data: r.Data}, rev, err
}

func (d countDefinitions) Commit(ctx context.Context, c goal.DefinitionChange, person, agent string) (string, error) {
	return d.repo.Commit(ctx, records.Change{ID: c.ID, Base: c.Base, Path: c.Path, Data: c.Data}, records.Actor{Person: person, Agent: agent})
}
