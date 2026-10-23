// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

type progressLineScenario struct {
	Goal struct {
		ID, Summary, Done, Scope string
	} `json:"goal"`
	Steps []struct {
		ID, Name  string
		DependsOn []string `json:"depends_on"`
	} `json:"steps"`
	IdleGoal struct {
		ID, Summary, Done, Step string
	} `json:"idle_goal"`
	LimitMS int64 `json:"limit_ms"`
	QuietMS int64 `json:"quiet_ms"`
}

// TestM3ProgressLinePublished (m3/progress-line.json) runs a headless Core over
// a goal whose first step lands from an injected worker result. The subscribed
// progress topic must carry the goal line's Now, start instant and stage
// entries, publish the changed line within the contract's 250 ms of that
// commit, and publish nothing for a recomputation that changes no line.
//
// The second goal waits for a worker the fixture host does not offer, so the
// frame that reports the landing can be checked for exactly one changed line.
func TestM3ProgressLinePublished(t *testing.T) {
	raw, err := os.ReadFile("../m3/progress-line.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario progressLineScenario
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.LimitMS <= 0 || scenario.QuietMS <= 0 || len(scenario.Steps) == 0 {
		t.Fatalf("incomplete scenario: %+v", scenario)
	}
	root := t.TempDir()
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "system", "core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"progress-line-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	world := &progressLineWorld{release: make(chan struct{}), root: t.TempDir()}
	var engine *work.Engine
	var mu sync.Mutex
	var landed time.Time
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) { engine = e; return world, nil },
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "progress-line-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"background": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := progressLineAgent(ctx, root)
			if err != nil {
				return err
			}
			conversation := "main:" + agent
			if err = e.Admit(ctx, work.Goal{ID: scenario.Goal.ID, Person: person, Agent: agent, Conversation: conversation, Summary: scenario.Goal.Summary, Done: scenario.Goal.Done, NoServiceReason: "plan", Scope: work.GoalScope{Repositories: []string{"fixture"}, Includes: scenario.Goal.Scope}, Criteria: []work.Criterion{{ID: "outcome", Statement: scenario.Goal.Done, Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "acceptance", Target: "fixture"}}}}); err != nil {
				return fmt.Errorf("admit workshop: %w", err)
			}
			steps := make([]work.Step, 0, len(scenario.Steps))
			for _, step := range scenario.Steps {
				steps = append(steps, work.Step{ID: step.ID, Name: step.Name, Kind: work.BackgroundStep, Done: step.Name, Repository: "fixture", DependsOn: step.DependsOn, Resources: work.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, Background: &work.BackgroundBrief{Goal: step.Name, Scope: "workshop plan", NotTouch: "files", Done: step.Name, Result: "notes", Checks: []work.BackgroundCheck{{Name: step.ID, Kind: "claimed"}}}})
			}
			if _, err = e.SubmitStepGraph(ctx, scenario.Goal.ID, person, agent, conversation, 0, steps); err != nil {
				return fmt.Errorf("submit graph: %w", err)
			}
			err = e.Admit(ctx, work.Goal{ID: scenario.IdleGoal.ID, Person: person, Agent: agent, Conversation: conversation, Summary: scenario.IdleGoal.Summary, Done: scenario.IdleGoal.Done, NoServiceReason: "plan", Scope: work.GoalScope{Repositories: []string{"fixture"}, Includes: scenario.IdleGoal.Step}, Criteria: []work.Criterion{{ID: "outcome", Statement: scenario.IdleGoal.Done, Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "acceptance", Target: "fixture"}}}, Tasks: []work.Task{{ID: "read", Done: scenario.IdleGoal.Done, Repository: "fixture", Base: "dev", Area: "read.md", Worker: "unavailable", Resources: work.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, Stage: work.TaskReady}}})
			if err != nil {
				return fmt.Errorf("admit idle: %w", err)
			}
			if err = e.Reconcile(ctx); err != nil {
				return fmt.Errorf("reconcile: %w", err)
			}
			return nil
		},
		Changed: func(_ context.Context, s work.State) error {
			mu.Lock()
			defer mu.Unlock()
			if !landed.IsZero() {
				return nil
			}
			for _, g := range s.Goals {
				if g.ID != scenario.Goal.ID {
					continue
				}
				for _, task := range g.Tasks {
					if task.ID == scenario.Steps[0].ID && task.Stage == work.TaskDone {
						landed = time.Now()
					}
				}
			}
			return nil
		},
	}}
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		err := <-done
		if err != nil && !errors.Is(err, context.Canceled) && !t.Failed() {
			t.Errorf("Core shutdown: %v", err)
		}
	})
	socket := filepath.Join(root, "run/client.sock")
	var c *clientapi.Client
	for attempt := 0; ; attempt++ {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("Core stopped before serving: %v", err)
		default:
		}
		c, err = clientapi.DialWithCapabilities(ctx, socket, "progress-line", []protocol.Capability{{Name: "progress", Version: "1.0"}})
		if err == nil {
			break
		}
		if ctx.Err() != nil || attempt > 1500 {
			t.Fatalf("Core socket: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer c.Close()
	// One reader owns the connection; every wait is a select on its frames, so
	// a short wait never closes the client.
	reads := make(chan progressLineRead)
	go func() {
		for {
			envelope, err := c.Receive(ctx)
			select {
			case reads <- progressLineRead{envelope, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	next := func(wait time.Duration) (protocol.Envelope, error) {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case r := <-reads:
			return r.envelope, r.err
		case <-timer.C:
			return protocol.Envelope{}, errProgressLineWait
		case <-ctx.Done():
			return protocol.Envelope{}, ctx.Err()
		}
	}
	// Core serves the socket before the fixture admits its goals; wait for the
	// projection to hold both lines before subscribing.
	view := "overview"
	for {
		if _, err = c.Progress(ctx, protocol.ProgressGet{View: &view}); err != nil {
			t.Fatalf("progress request: %v", err)
		}
		probe, receiveErr := next(10 * time.Second)
		if receiveErr != nil {
			t.Fatalf("progress read: %v", receiveErr)
		}
		if p, ok := probe.Payload.(protocol.Progress); ok && progressLineWorking(&p, scenario.Goal.ID, scenario.Steps[0].Name) {
			break
		}
		select {
		case runErr := <-done:
			done <- runErr
			t.Fatalf("Core stopped before admitting work: %v", runErr)
		case <-ctx.Done():
			t.Fatalf("fixture goals never appeared: %+v", probe.Payload)
		case <-time.After(20 * time.Millisecond):
		}
	}
	topic := "progress"
	if _, err = c.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := next(10 * time.Second)
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if err != nil || frame.Error != nil || !ok || len(snapshot.Items) != 1 {
		select {
		case runErr := <-done:
			done <- runErr
			t.Fatalf("progress snapshot after Core stop: %+v %v (%v)", frame, err, runErr)
		default:
		}
		t.Fatalf("progress snapshot: %+v %v", frame, err)
	}
	var before protocol.ProgressUpdate
	if err = json.Unmarshal(snapshot.Items[0].Payload, &before); err != nil {
		t.Fatal(err)
	}
	assertLine := func(update protocol.ProgressUpdate) *protocol.ProgressLine {
		t.Helper()
		if update.Rows == nil || len(*update.Rows) != 2 {
			t.Fatalf("progress rows = %+v, want the landing goal and the idle goal", update.Rows)
		}
		for _, row := range *update.Rows {
			if row.Ref == scenario.Goal.ID {
				line := row.Line
				return &line
			}
		}
		t.Fatalf("goal line missing: %+v", update.Rows)
		return nil
	}
	line := assertLine(before)
	if line.StartedAt == nil || line.Now == nil || line.Stages == nil {
		t.Fatalf("line lacks Now, start or stage entries: %+v", line)
	}
	if *line.Now != scenario.Steps[0].Name {
		t.Fatalf("now = %q, want the working step %q", *line.Now, scenario.Steps[0].Name)
	}
	if len(*line.Stages) != len(scenario.Steps)+1 || (*line.Stages)[0].Number == nil || *(*line.Stages)[0].Number != "1" {
		t.Fatalf("stage entries = %+v", *line.Stages)
	}
	if state := (*line.Stages)[0].State; state != protocol.ProgressStepStateWorking {
		t.Fatalf("first stage state = %s, want working", state)
	}
	close(world.release)
	var changed protocol.ProgressUpdate
	var published time.Time
	for {
		frame, err = next(10 * time.Second)
		if err != nil || frame.Error != nil {
			t.Fatalf("progress update: %+v %v", frame, err)
		}
		update, ok := frame.Payload.(protocol.ProgressUpdate)
		if !ok || update.Rows == nil {
			continue
		}
		if reflect.DeepEqual(update.Rows, before.Rows) {
			continue
		}
		changed, published = update, time.Now()
		break
	}
	mu.Lock()
	committed := landed
	mu.Unlock()
	if committed.IsZero() {
		t.Fatal("the fixture result never committed a landing")
	}
	latency := published.Sub(committed)
	if latency < 0 || latency > time.Duration(scenario.LimitMS)*time.Millisecond {
		t.Fatalf("progress published %v after the commit, want within %d ms", latency, scenario.LimitMS)
	}
	landedRows := *changed.Rows
	if len(landedRows) != len(*before.Rows) {
		t.Fatalf("frame row count changed: %d -> %d", len(*before.Rows), len(landedRows))
	}
	differs := 0
	for i, row := range landedRows {
		if !reflect.DeepEqual(row, (*before.Rows)[i]) {
			differs++
		}
	}
	if differs != 1 {
		t.Fatalf("frame changed %d lines, want exactly one: %+v", differs, landedRows)
	}
	after := assertLine(changed)
	if after.Stage == line.Stage {
		t.Fatalf("landing left the stage %q", after.Stage)
	}
	if after.Stages == nil || len(*after.Stages) != len(*line.Stages) {
		t.Fatalf("landing lost stage entries: %+v", after.Stages)
	}
	if state := (*after.Stages)[0].State; state != protocol.ProgressStepStateDone {
		t.Fatalf("landed stage state = %s, want done", state)
	}
	// A recomputation that changes no line publishes nothing. The host report
	// commits new node data; no line carries it. Later steps keep moving after
	// the first landing, so wait for a quiet target before the no-op commit.
	if engine == nil {
		t.Fatal("work engine missing")
	}
	stable := changed
	for {
		frame, readErr := next(300 * time.Millisecond)
		if errors.Is(readErr, errProgressLineWait) {
			break
		}
		if readErr != nil || frame.Error != nil {
			t.Fatalf("settle progress: %+v %v", frame, readErr)
		}
		if update, ok := frame.Payload.(protocol.ProgressUpdate); ok && update.Rows != nil {
			stable = update
		}
	}
	host, err := cfg.Work.MainHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host.ObservedAt = time.Now().Unix()
	host.LoadCPU += 0.25
	if err = engine.NodeChanged(ctx, host); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err = next(time.Duration(scenario.QuietMS) * time.Millisecond)
		if errors.Is(err, errProgressLineWait) {
			break
		}
		if err != nil || frame.Error != nil {
			t.Fatalf("quiet progress: %+v %v", frame, err)
		}
		if update, ok := frame.Payload.(protocol.ProgressUpdate); ok && update.Rows != nil && !reflect.DeepEqual(update.Rows, stable.Rows) {
			t.Fatalf("unchanged work published a frame: %+v", *update.Rows)
		}
	}
}

// progressLineWorking reports whether the goal line already shows its first
// step as the work in progress, which means the fixture host reported the
// worker and the assignment was planned.
func progressLineWorking(p *protocol.Progress, goal, step string) bool {
	if p.Rows == nil || len(*p.Rows) != 2 {
		return false
	}
	for _, row := range *p.Rows {
		if row.Ref == goal {
			return row.Line.Now != nil && *row.Line.Now == step
		}
	}
	return false
}

// progressLineAgent reads the starter agent an installed Core seeds, which is
// the agent whose work method dispatches work.
func progressLineAgent(ctx context.Context, root string) (string, error) {
	db, err := canon.OpenSQLiteReader(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	var id string
	err = db.QueryRowContext(ctx, "SELECT agent_id FROM agent_starters WHERE preset='dev'").Scan(&id)
	return id, err
}

var errProgressLineWait = errors.New("progress read timed out")

type progressLineRead struct {
	envelope protocol.Envelope
	err      error
}

type progressLineWorld struct {
	release chan struct{}
	root    string
}

func (w *progressLineWorld) Authorize(context.Context, work.Action) error { return nil }

func (w *progressLineWorld) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, errors.New("unexpected observation")
}

func (w *progressLineWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	if a.Kind != work.Assign || a.StepKind != work.BackgroundStep {
		<-ctx.Done()
		return work.Observation{}, ctx.Err()
	}
	select {
	case <-w.release:
	case <-ctx.Done():
		return work.Observation{}, ctx.Err()
	}
	proof := filepath.Join(w.root, a.TaskID+".txt")
	if err := os.WriteFile(proof, []byte("workshop drafted\n"), 0600); err != nil {
		return work.Observation{}, err
	}
	return work.Observation{Status: work.OutcomeSucceeded, EffectsSettled: true, Proof: work.Proof{Revision: "result-" + a.TaskID, Ref: proof, Passed: true}}, nil
}
