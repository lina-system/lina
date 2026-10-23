// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// This child does actual work only after the scenario releases its stage.
func TestM3WorkPanelStageWorker(t *testing.T) {
	if os.Getenv("LINA_PANEL_STAGE_WORKER") != "1" {
		return
	}
	fmt.Println("ready")
	input := bufio.NewScanner(os.Stdin)
	if input.Scan() && input.Text() == "finish" {
		if err := os.WriteFile(os.Getenv("LINA_PANEL_STAGE_RESULT"), []byte("stage worker result"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

type panelStageAdapter struct {
	worker.Adapter
	events chan worker.Event
	done   <-chan struct{}
}

func (a *panelStageAdapter) Events() <-chan worker.Event { return a.events }
func (a *panelStageAdapter) Snapshot(_ context.Context, thread worker.Thread) (worker.ThreadSnapshot, error) {
	state := worker.Active
	select {
	case <-a.done:
		state = worker.Dead
	default:
	}
	return worker.ThreadSnapshot{Thread: thread, State: state, Worker: "stub"}, nil
}

type panelStageWorld struct {
	root                     string
	sessions                 *worker.Live
	roots                    map[string]bool
	releaseRoots, releaseAll chan struct{}
	mu                       sync.Mutex
	results                  []string
}

func (*panelStageWorld) Authorize(context.Context, goal.Action) error { return nil }
func (*panelStageWorld) Observe(context.Context, goal.Action) (goal.Observation, error) {
	return goal.Observation{}, errors.New("unexpected recovery")
}
func (w *panelStageWorld) Execute(ctx context.Context, a goal.Action) (goal.Observation, error) {
	if a.Kind != goal.Assign {
		<-ctx.Done()
		return goal.Observation{}, ctx.Err()
	}
	self, err := os.Executable()
	if err != nil {
		return goal.Observation{}, err
	}
	output := filepath.Join(w.root, a.ID+".txt")
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestM3WorkPanelStageWorker$")
	cmd.Env = []string{"GOMAXPROCS=1", "LINA_PANEL_STAGE_WORKER=1", "LINA_PANEL_STAGE_RESULT=" + output}
	cmd.Dir = w.root
	input, err := cmd.StdinPipe()
	if err != nil {
		return goal.Observation{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return goal.Observation{}, err
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		return goal.Observation{}, err
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		input.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return goal.Observation{}, fmt.Errorf("stub worker did not become ready: %v", scanner.Err())
	}
	done := make(chan struct{})
	adapter := &panelStageAdapter{events: make(chan worker.Event), done: done}
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done); close(adapter.events) }()
	closeWorker := func(ctx context.Context) error {
		_ = input.Close()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			return nil
		}
	}
	defer closeWorker(context.Background())
	thread := worker.Thread{ID: a.ID, TurnID: "turn", TaskID: a.TaskID}
	if err := w.sessions.Adopt(worker.Session{ID: a.ID, Worker: "stub", Thread: thread, Started: time.Now()}, adapter, closeWorker); err != nil {
		return goal.Observation{}, err
	}
	release := w.releaseAll
	if w.roots[a.GoalID+":"+a.TaskID] {
		release = w.releaseRoots
	}
	select {
	case <-release:
	case <-ctx.Done():
		return goal.Observation{}, ctx.Err()
	}
	if _, err = fmt.Fprintln(input, "finish"); err != nil {
		return goal.Observation{}, err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return goal.Observation{}, ctx.Err()
	}
	if waitErr != nil {
		return goal.Observation{}, waitErr
	}
	content, err := os.ReadFile(output)
	if err != nil || string(content) != "stage worker result" {
		return goal.Observation{}, fmt.Errorf("worker result missing: %v", err)
	}
	w.mu.Lock()
	w.results = append(w.results, output)
	w.mu.Unlock()
	return goal.Observation{Status: goal.OutcomeSucceeded, EffectsSettled: true, Proof: goal.Proof{Revision: "result-" + a.ID, Ref: output, Passed: true}}, nil
}

// Uses #768's corpus, manager admission and E6/E7 evaluators. The fixture
// driver starts real stub processes; installed Core owns their ledger states
// and live registry, and the installed TUI runs in a real PTY.
func TestM3WorkPanelStages(t *testing.T) {
	raw, err := os.ReadFile("../m3/work-panel-stages.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Widths    []int
		Height    int
		Languages []design.Language
		Glyphs    []string
		Result    string
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	fixture := loadPanelFixture(t)
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	h.ctx = ctx
	core := start(true)
	h.stopCore(core)
	fixture.States = []string{"waiting"}
	cards := seedPanelCorpus(t, h, fixture, len(fixture.Graphs), 1)
	var main struct {
		ID string `json:"main_id"`
	}
	raw, err = os.ReadFile(filepath.Join(h.root, "system/core/main.json"))
	if err != nil || json.Unmarshal(raw, &main) != nil {
		t.Fatal("main identity", err)
	}
	sessions := worker.NewLive()
	world := &panelStageWorld{root: t.TempDir(), sessions: sessions, roots: map[string]bool{}, releaseRoots: make(chan struct{}), releaseAll: make(chan struct{})}
	route, err := modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	cfg := boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route,
		AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step == boot.Ready {
				close(ready)
			}
			return nil
		},
		Work: &boot.WorkConfig{Sessions: sessions,
			Driver: func(*goal.Engine) (goal.Driver, error) { return world, nil },
			MainHost: func(context.Context) (goal.Node, error) {
				return goal.Node{ID: main.ID, Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 8, CPULimit: 8, AvailableMemoryMB: 16000, MemoryLimitMB: 16000, AvailableDiskMB: 16000, DiskLimitMB: 16000, Workers: map[string]bool{"background": true}}, nil
			},
			Ready: func(ctx context.Context, e *goal.Engine, person, _ string) error {
				state, err := e.Snapshot(ctx)
				if err != nil {
					return err
				}
				byID := map[string]goal.Goal{}
				for _, g := range state.Goals {
					byID[g.ID] = g
				}
				for i, card := range cards {
					g := byID[card.Goal]
					steps, err := expandPanelGraph(fixture.Graphs[i])
					if err != nil {
						return err
					}
					revision := make([]goal.Step, 0, len(steps))
					for _, step := range steps {
						id := fmt.Sprintf("s%d", step.Index)
						item := goal.Step{ID: id, Name: step.Name, Kind: goal.BackgroundStep, Done: "Produce " + step.Name, Repository: "fixture", Resources: goal.Resources{CPU: 0.01, MemoryMB: 1, DiskMB: 1}, Background: &goal.BackgroundBrief{Goal: step.Name, Scope: fixture.Graphs[i].ID, NotTouch: "other work", Done: step.Name, Result: "notes", Checks: []goal.BackgroundCheck{{Name: id, Kind: "claimed"}}}}
						for _, after := range step.After {
							item.DependsOn = append(item.DependsOn, fmt.Sprintf("s%d", after))
						}
						if len(step.After) == 0 {
							world.roots[g.ID+":"+id] = true
						}
						revision = append(revision, item)
					}
					if _, err = e.SubmitStepGraph(ctx, g.ID, person, g.Agent, g.Conversation, g.StepGraph.Revision, revision); err != nil {
						return err
					}
				}
				return nil
			},
		},
	}
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		err := <-done
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Core shutdown: %v", err)
		}
		if err := sessions.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-ready:
	case err := <-done:
		done <- err
		t.Fatalf("Core failed: %v", err)
	case <-ctx.Done():
		t.Fatal("Core readiness", ctx.Err())
	}
	awaitPanelStages(t, h, func(rows []protocol.ProgressRow) bool { return panelWorkerCount(rows) == 6 })
	recording := &panelRecording{}
	for phase := 0; phase < 2; phase++ {
		if phase == 1 {
			close(world.releaseRoots)
			awaitPanelStages(t, h, func(rows []protocol.ProgressRow) bool { return panelWorkerCount(rows) == 87 })
		}
		projection := panelProjection(t, h)
		for _, language := range scenario.Languages {
			for _, glyph := range scenario.Glyphs {
				for _, width := range scenario.Widths {
					c := panelCase{Name: fmt.Sprintf("phase=%d/%s/%s/%d", phase, language, glyph, width), N: len(cards), Language: language, ASCII: glyph == "ascii", Width: width, Height: scenario.Height}
					recordPanelStageViews(t, h, &c, projection, phase == 0)
					recording.Cases = append(recording.Cases, c)
				}
			}
		}
	}
	for _, check := range []func(*panelRecording) panelVerdict{checkNoGraph, checkWorkers} {
		v := check(recording)
		if !v.Input || !v.Passed {
			t.Fatalf("%s: %s", v.Check, v.Detail)
		}
		t.Logf("%s: %s", v.Check, v.Detail)
	}
	close(world.releaseAll)
	landed := awaitPanelStages(t, h, func(rows []protocol.ProgressRow) bool {
		if len(rows) != len(cards) || panelWorkerCount(rows) != 0 {
			return false
		}
		for _, row := range rows {
			if row.Line.Stages == nil {
				return false
			}
			for _, stage := range *row.Line.Stages {
				if stage.Kind == "stage" && stage.State != "done" {
					return false
				}
			}
		}
		return true
	})
	if len(landed) != 5 {
		t.Fatal("missing landed work")
	}
	world.mu.Lock()
	count := len(world.results)
	world.mu.Unlock()
	if count != 101 {
		t.Fatalf("verified %d worker artifacts, want all 101 corpus steps", count)
	}
	t.Logf("Core admitted all 101 worker artifacts; no landed step retained a worker row")
}

func panelWorkerCount(rows []protocol.ProgressRow) int {
	n := 0
	for _, row := range rows {
		if row.Line.Stages == nil {
			continue
		}
		for _, stage := range *row.Line.Stages {
			if stage.Steps == nil {
				continue
			}
			for _, step := range *stage.Steps {
				if step.Workers != nil {
					n += len(*step.Workers)
				}
			}
		}
	}
	return n
}

func awaitPanelStages(t *testing.T, h *harness, accept func([]protocol.ProgressRow) bool) []protocol.ProgressRow {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "panel-stages", []protocol.Capability{{Name: "progress", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	topic := "progress"
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	var latest []protocol.ProgressRow
	for {
		e, err := c.Receive(h.ctx)
		if err != nil || e.Error != nil {
			t.Fatalf("stage stream: %v %+v, last workers=%d", err, e.Error, panelWorkerCount(latest))
		}
		var update protocol.ProgressUpdate
		switch p := e.Payload.(type) {
		case protocol.Snapshot:
			for _, item := range p.Items {
				if item.Kind == protocol.KindProgressUpdate {
					if err := json.Unmarshal(item.Payload, &update); err != nil {
						t.Fatal(err)
					}
				}
			}
		case protocol.ProgressUpdate:
			update = p
		default:
			continue
		}
		if update.Rows == nil {
			continue
		}
		latest = nil
		for _, row := range *update.Rows {
			if row.Line.Kind != "goal" {
				latest = append(latest, row)
			}
		}
		if accept(latest) {
			return latest
		}
	}
}

func recordPanelStageViews(t *testing.T, h *harness, c *panelCase, projection []protocol.ProgressRow, requireWorker bool) {
	t.Helper()
	env := []string{"TERM=xterm-256color", "LANG=" + string(c.Language) + "_US.UTF-8", "LC_ALL=" + string(c.Language) + "_US.UTF-8"}
	if c.ASCII {
		env = append(env, "LINA_GLYPHS=ascii")
	}
	terminal := h.startTerminalSize(env, false, true, c.Width, c.Height)
	x, width := 80, 40
	if c.Width < 120 {
		x, width = 0, c.Width
	}
	_, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return s.AltScreen && !s.CursorHidden && strings.Contains(screenText(s), design.Text(c.Language, "live"))
	})
	if err != nil {
		t.Fatal(err)
	}
	h.keys(terminal, "\x10")
	for i, row := range projection {
		if row.Line.Stages == nil {
			t.Fatalf("card %s has no stages", row.Ref)
		}
		if i > 0 {
			h.keys(terminal, "\x1b[B")
		}
		h.keys(terminal, "\r")
		for _, expanded := range []bool{false, true} {
			if expanded {
				h.keys(terminal, "\r")
			}
			snap, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
				panel := panelCellRows(s, x, width)
				back := "‹ "
				if c.ASCII {
					back = "< "
				}
				if len(panel) < 3 || !strings.HasPrefix(panel[1], back) || !strings.Contains(panel[1], row.Line.Name) {
					return false
				}
				body := strings.Join(panel[2:c.Height-4], "\n")
				if expanded && panelRowHasWorker(row) {
					return strings.Contains(body, "[stub]")
				}
				return !strings.Contains(body, "[stub]") && strings.Contains(body, (*row.Line.Stages)[0].Label)
			})
			if err != nil {
				t.Fatalf("%s card %s expanded=%v: %v\n%s", c.Name, row.Ref, expanded, err, screenText(snap))
			}
			view := panelStageView{Card: row.Ref}
			panel := panelCellRows(snap, x, width)
			for _, line := range panel[2 : c.Height-4] {
				if strings.TrimSpace(line) != "" {
					view.Rows = append(view.Rows, line)
				}
			}
			for _, stage := range *row.Line.Stages {
				entry := panelStageEntry{Kind: stage.Kind, Label: stage.Label, State: stage.State, Open: stage.State != "done" && stage.State != "next"}
				if stage.Number != nil {
					entry.Number, _ = strconv.Atoi(stage.Number.String())
				}
				if stage.Steps != nil {
					for _, step := range *stage.Steps {
						entry.Steps = append(entry.Steps, panelStageStep{Name: step.Name, State: step.State})
					}
				}
				view.Entries = append(view.Entries, entry)
			}
			number, step := 0, ""
			for _, line := range view.Rows {
				if panelEntryLine(line) {
					parts := entryRow.FindStringSubmatch(strings.TrimSpace(line))
					number, _ = strconv.Atoi(parts[1])
					step = ""
					continue
				}
				if strings.HasPrefix(line, "        [") {
					view.Workers = append(view.Workers, panelWorkerRow{Text: strings.TrimSpace(line), Entry: number, Step: step, Expanded: expanded && step != ""})
					continue
				}
				if strings.HasPrefix(line, "    ") {
					parts := entryRow.FindStringSubmatch(strings.TrimSpace(line))
					if parts != nil {
						step = parts[3]
					} else {
						step = ""
					}
				}
			}
			if !expanded && len(view.Workers) > 0 {
				t.Fatal("collapsed stage leaked worker")
			}
			if requireWorker && expanded && len(view.Workers) == 0 {
				t.Fatal("E7 has no positive worker observation")
			}
			c.Views = append(c.Views, view)
			t.Logf("%s card=%s expanded=%v\n%s", c.Name, row.Ref, expanded, strings.Join(panel, "\n"))
		}
		// The last single-step goal has no open steps once it enters checking.
		if panelRowHasWorker(row) {
			h.keys(terminal, "\x1b")
		}
		h.keys(terminal, "\x1b")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
}

func panelRowHasWorker(row protocol.ProgressRow) bool {
	return panelWorkerCount([]protocol.ProgressRow{row}) > 0
}
