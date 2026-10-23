// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type panelStepExecution struct {
	action  goal.Action
	release chan struct{}
}

type panelStepWorkers struct {
	root       string
	mu         sync.Mutex
	executions map[string]panelStepExecution
	changed    chan struct{}
}

func (w *panelStepWorkers) Authorize(context.Context, goal.Action) error { return nil }
func (w *panelStepWorkers) Observe(context.Context, goal.Action) (goal.Observation, error) {
	return goal.Observation{}, fmt.Errorf("unexpected worker observation")
}
func (w *panelStepWorkers) Execute(ctx context.Context, a goal.Action) (goal.Observation, error) {
	if a.Kind != goal.Assign || a.StepKind != goal.BackgroundStep {
		<-ctx.Done()
		return goal.Observation{}, ctx.Err()
	}
	key := a.GoalID + "/" + a.TaskID
	w.mu.Lock()
	if _, exists := w.executions[key]; exists {
		w.mu.Unlock()
		return goal.Observation{}, fmt.Errorf("duplicate worker %s", key)
	}
	execution := panelStepExecution{a, make(chan struct{})}
	w.executions[key] = execution
	close(w.changed)
	w.changed = make(chan struct{})
	w.mu.Unlock()
	select {
	case <-execution.release:
	case <-ctx.Done():
		return goal.Observation{}, ctx.Err()
	}
	path := filepath.Join(w.root, a.GoalID+"-"+a.TaskID+".txt")
	if err := os.WriteFile(path, []byte("step result verified\n"), 0600); err != nil {
		return goal.Observation{}, err
	}
	// The stub produces an actual result; Core accepts its observation through
	// Report, records the landed step, and wakes its normal progress publisher.
	return goal.Observation{Status: goal.OutcomeSucceeded, EffectsSettled: true, Proof: goal.Proof{Revision: a.ID, Ref: path, Passed: true}}, nil
}
func (w *panelStepWorkers) wait(ctx context.Context, goalID, step string) (panelStepExecution, error) {
	for {
		w.mu.Lock()
		execution, ok := w.executions[goalID+"/"+step]
		changed := w.changed
		w.mu.Unlock()
		if ok {
			return execution, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return panelStepExecution{}, ctx.Err()
		}
	}
}

type panelLatencyTerminal struct {
	name     string
	terminal *terminal
	probe    *panelFrameProbe
	x, width int
	selected string
	before   tuiterm.Snapshot
	anchor   []string
	samples  []panelLatencySample
	record   panelCase
}

func (v *panelLatencyTerminal) now(s tuiterm.Snapshot) string {
	rows := panelCellRows(s, v.x, v.width)
	for i := 1; i+1 < len(rows); i++ {
		if strings.HasPrefix(rows[i], v.selected) {
			return strings.TrimSpace(rows[i+1])
		}
	}
	return ""
}

// TestM3WorkPanelEventLatency injects 50 real step landings with 50 running
// graphs, through Core boot, its ledger and topic, and eight real TUI PTYs.
// All graphs/names come from the T-16 corpus. The fan-out's 82 parallel steps
// let 50 landings change Now while every graph remains in progress.
func TestM3WorkPanelEventLatency(t *testing.T) {
	fixture := loadPanelFixture(t)
	raw, err := os.ReadFile("../m3/work-panel-latency.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		panelLatencyBudget
		N int `json:"count"`
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.N != 50 || config.Count != 50 {
		t.Fatal("E8 requires 50 work items and 50 transitions")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines := make([]string, 80)
		for i := range lines {
			lines[i] = fmt.Sprintf("ANCHOR-%03d synthetic conversation", i)
		}
		emitResponse(w, strings.Join(lines, "\n"), true)
	}))
	defer server.Close()
	h := newHarness(t, buildLina(t))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	h.ctx = ctx
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	manifest, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"system/bin", "system/core"} {
		if err = os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"latency-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	route, err := modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": server.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	workers := &panelStepWorkers{root: t.TempDir(), executions: map[string]panelStepExecution{}, changed: make(chan struct{})}
	ready := make(chan struct{})
	coreCtx, stop := context.WithCancel(ctx)
	core := &process{done: make(chan struct{})}
	go func() {
		core.err = boot.Run(coreCtx, boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route, Work: &boot.WorkConfig{
			Driver: func(*goal.Engine) (goal.Driver, error) { return workers, nil },
			MainHost: func(context.Context) (goal.Node, error) {
				return goal.Node{ID: "latency-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 1000, CPULimit: 1000, AvailableMemoryMB: 100000, MemoryLimitMB: 100000, AvailableDiskMB: 100000, DiskLimitMB: 100000, Workers: map[string]bool{"background": true}}, nil
			},
			Ready: func(ctx context.Context, e *goal.Engine, person, _ string) error {
				agent, err := progressLineAgent(ctx, h.root)
				if err != nil {
					return err
				}
				for i := 0; i < config.N; i++ {
					id := fmt.Sprintf("latency-%03d", i)
					graph := fixture.Graphs[i%len(fixture.Graphs)]
					if err = e.Admit(ctx, goal.Goal{ID: id, Person: person, Agent: agent, Conversation: "main:" + agent, Summary: fixture.Names[i%len(fixture.Names)], Done: "The sample is verified", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"fixture"}, Includes: graph.ID}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "The sample is verified", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "fixture"}}}}); err != nil {
						return err
					}
					expanded, err := expandPanelGraph(graph)
					if err != nil {
						return err
					}
					steps := make([]goal.Step, 0, len(expanded))
					for _, step := range expanded {
						item := goal.Step{ID: fmt.Sprintf("s%d", step.Index), Name: step.Name, Kind: goal.BackgroundStep, Done: "Produce " + step.Name, Repository: "fixture", Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, Background: &goal.BackgroundBrief{Goal: step.Name, Scope: graph.ID, NotTouch: "other work", Done: "Produce " + step.Name, Result: "notes", Checks: []goal.BackgroundCheck{{Name: "result", Kind: "claimed"}}}}
						for _, after := range step.After {
							item.DependsOn = append(item.DependsOn, fmt.Sprintf("s%d", after))
						}
						steps = append(steps, item)
					}
					if _, err = e.SubmitStepGraph(ctx, id, person, agent, "main:"+agent, 0, steps); err != nil {
						return err
					}
				}
				close(ready)
				return nil
			},
		}})
		close(core.done)
	}()
	defer func() {
		stop()
		select {
		case <-core.done:
			if core.err != nil {
				t.Error(core.err)
			}
		case <-time.After(operationTimeout):
			t.Error("Core shutdown timed out")
		}
	}()
	h.connectWithin(core, 30*time.Second).Close()
	select {
	case <-ready:
	case <-core.done:
		t.Fatalf("Core setup: %v", core.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCoreReady(t, h)
	client, conversation := conversationClient(t, h)
	defer client.Close()
	admittedTurn(t, h, client, conversation, "latency-history", "Show the synthetic conversation.")
	target := "latency-003"
	first, err := workers.wait(ctx, target, "s0")
	if err != nil {
		t.Fatal(err)
	}
	close(first.release)
	for i := 1; i <= 82; i++ {
		if _, err = workers.wait(ctx, target, fmt.Sprintf("s%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// Worker entry is an explicit start barrier. Once all graphs' first steps
	// have entered, the recorded state is in progress, not synthetic card data.
	for i := 0; i < config.N; i++ {
		if _, err = workers.wait(ctx, fmt.Sprintf("latency-%03d", i), "s0"); err != nil {
			t.Fatal(err)
		}
	}
	assertRunning := func() []protocol.ProgressRow {
		t.Helper()
		rows := panelProjection(t, h)
		if len(rows) != config.N {
			t.Fatalf("Core has %d work cards; want %d", len(rows), config.N)
		}
		for _, row := range rows {
			if row.Line.State == nil || *row.Line.State != protocol.RunningStateInProgress {
				t.Fatalf("work not in progress: %+v", row)
			}
		}
		return rows
	}
	cards := assertRunning()
	index := -1
	var current protocol.ProgressLine
	for i, row := range cards {
		if row.Ref == target {
			index, current = i, row.Line
		}
	}
	if index < 0 || current.Now == nil {
		t.Fatal("fan work card missing")
	}
	var views []*panelLatencyTerminal
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			for _, width := range fixture.Widths {
				env := []string{"TERM=xterm-256color", "LANG=" + string(language) + "_US.UTF-8", "LC_ALL=" + string(language) + "_US.UTF-8"}
				mark := "› "
				if ascii {
					env = append(env, "LINA_GLYPHS=ascii")
					mark = "> "
				}
				v := &panelLatencyTerminal{name: fmt.Sprintf("%s/ascii=%t/%dx%d", language, ascii, width, fixture.Height), probe: &panelFrameProbe{}, x: 80, width: 40, selected: mark + current.Name}
				if width < 120 {
					v.x, v.width = 0, width
				}
				v.terminal = h.startTerminalObserved(env, false, true, width, fixture.Height, v.probe.observe)
				wait := func(match func(tuiterm.Snapshot) bool) tuiterm.Snapshot {
					t.Helper()
					c, cancel := context.WithTimeout(ctx, operationTimeout)
					defer cancel()
					s, err := v.terminal.screen.Wait(c, match)
					if err != nil {
						t.Fatalf("%s setup: %v\n%s", v.name, err, screenText(s))
					}
					return s
				}
				wait(func(s tuiterm.Snapshot) bool { return !s.CursorHidden && strings.Contains(screenText(s), "ANCHOR-079") })
				h.keys(v.terminal, "keep draft")
				wait(func(s tuiterm.Snapshot) bool { return strings.Contains(screenText(s), "keep draft") })
				h.keys(v.terminal, "\x1b[<64;10;10M")
				scrolled := wait(func(s tuiterm.Snapshot) bool {
					return !strings.Contains(screenText(s), "ANCHOR-079") && strings.Contains(screenText(s), "ANCHOR-")
				})
				conversationWidth := width
				if width >= 120 {
					conversationWidth = 79
				}
				v.anchor = panelCellRows(scrolled, 0, conversationWidth)[1 : fixture.Height-3]
				h.keys(v.terminal, "\x10"+strings.Repeat("\x1b[B", index))
				v.before = wait(func(s tuiterm.Snapshot) bool { return s.CursorHidden && v.now(s) == *current.Now })
				v.record = panelCase{Name: v.name, N: config.N, Language: language, ASCII: ascii, Width: width, Height: fixture.Height, Cards: []panelCard{{ID: target, Name: current.Name, Now: *current.Now}}}
				views = append(views, v)
			}
		}
	}
	db, err := canon.OpenSQLiteReader(ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	previous := *current.Now
	for i := 1; i <= config.Count; i++ {
		found := make([]<-chan panelObservedFrame, len(views))
		for j, v := range views {
			old := previous
			found[j] = v.probe.arm(func(s tuiterm.Snapshot) bool { now := v.now(s); return now != "" && now != old })
		}
		execution, err := workers.wait(ctx, target, fmt.Sprintf("s%d", i))
		if err != nil {
			t.Fatal(err)
		}
		close(execution.release)
		observed := make([]panelObservedFrame, len(views))
		for j := range views {
			timer := time.NewTimer(operationTimeout)
			select {
			case observed[j] = <-found[j]:
			case <-timer.C:
				t.Fatalf("%s step %d missing from PTY", views[j].name, i)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			timer.Stop()
		}
		var event, stamp string
		if err = db.QueryRowContext(ctx, "SELECT id,json_extract(body,'$.event.recorded_at') FROM work_events WHERE kind='action.observed' AND id LIKE ? ORDER BY seq LIMIT 1", "action.observed:"+execution.action.ID+":%").Scan(&event, &stamp); err != nil {
			t.Fatal(err)
		}
		committed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range assertRunning() {
			if row.Ref == target {
				current = row.Line
			}
		}
		if current.Now == nil || *current.Now == previous {
			t.Fatal("step landing did not change Core Now")
		}
		proof := filepath.Join(workers.root, target+fmt.Sprintf("-s%d.txt", i))
		if data, err := os.ReadFile(proof); err != nil || string(data) != "step result verified\n" {
			t.Fatalf("missing worker result: %v", err)
		}
		for j, v := range views {
			frame := observed[j]
			if v.now(frame.screen) != *current.Now {
				t.Fatalf("%s first changed frame has %q, Core has %q", v.name, v.now(frame.screen), *current.Now)
			}
			v.samples = append(v.samples, panelLatencySample{event, committed, frame})
			t.Logf("E8 %s N=%d transition=%02d step=%s event=%s commit=%s first_frame=%s latency=%s writes=%d\n%s", v.name, config.N, i, execution.action.TaskID, event, stamp, frame.at.UTC().Format(time.RFC3339Nano), frame.at.Sub(committed), frame.screen.Writes, screenText(frame.screen))
			if frame.screen.CursorHidden != v.before.CursorHidden || v.now(frame.screen) == "" {
				t.Fatal("E9 focus or selection changed")
			}
			if v.x > 0 {
				if !strings.Contains(screenText(frame.screen), "keep draft") {
					t.Fatal("E9 draft changed")
				}
				if !reflect.DeepEqual(panelCellRows(frame.screen, 0, 79)[1:fixture.Height-3], v.anchor) {
					t.Fatal("E9 reading anchor changed")
				}
			}
			// Feed the actual observation into the shared T-16 E8/E9 evaluators.
			rows := screenRows(frame.screen)
			v.record.Frames = append(v.record.Frames, panelFrame{Label: "step", View: "cards", At: frame.at, Commit: committed, Rows: rows, Panel: panelCellRows(frame.screen, v.x, v.width), Composer: rows[fixture.Height-2], Anchor: strings.Join(v.anchor, "\n"), CursorHidden: frame.screen.CursorHidden})
			v.record.Moves = append(v.record.Moves, panelTransition{Card: target, From: previous, To: *current.Now, Commit: committed, Frame: len(v.record.Frames) - 1})
		}
		previous = *current.Now
	}
	for _, v := range views {
		p95, maximum, err := panelLatencySummary(v.samples, config.panelLatencyBudget)
		t.Logf("E8 %s N=%d samples=%d p95=%s max=%s", v.name, config.N, len(v.samples), p95, maximum)
		if err != nil {
			t.Error(err)
		}
		// The shared evaluator's parser assumes the 40-column sidebar; the
		// independent gate above covers every width with the same samples.
		if v.x > 0 {
			for _, verdict := range []panelVerdict{checkLatency(&panelRecording{Cases: []panelCase{v.record}}), checkCalm(&panelRecording{Cases: []panelCase{v.record}})} {
				if !verdict.Input || !verdict.Passed {
					t.Errorf("%s %s: %s", v.name, verdict.Check, verdict.Detail)
				}
			}
		}
		h.keys(v.terminal, "\x1b")
		c, stopWait := context.WithTimeout(ctx, operationTimeout)
		restored, err := v.terminal.screen.Wait(c, func(s tuiterm.Snapshot) bool {
			return !s.CursorHidden && strings.Contains(screenText(s), "keep draft") && strings.Contains(screenText(s), "ANCHOR-")
		})
		stopWait()
		if err != nil {
			t.Fatalf("E9 restore %s: %v", v.name, err)
		}
		width := v.width
		if v.x > 0 {
			width = 79
		}
		if !reflect.DeepEqual(panelCellRows(restored, 0, width)[1:fixture.Height-3], v.anchor) {
			t.Errorf("E9 %s restored reading anchor changed", v.name)
		}
		h.keys(v.terminal, "\x04")
		v.terminal.process.wait(t, 0)
		h.expectTerminalRestored(v.terminal)
	}
}
