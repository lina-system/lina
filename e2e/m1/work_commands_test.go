// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

// A work command is the person's choice about one goal. Core grants it only to
// an eligible decider, which is a process outside Core's own tree. The fixture
// therefore runs Core in a child process and sends the choices from this test,
// as a person's TUI would.
const workCommandsRoot = "LINA_WORK_COMMANDS_ROOT"

type workCommandScenario struct {
	Goals []struct {
		ID, Summary, Step string
	} `json:"goals"`
	QuietMS int64 `json:"quiet_ms"`
}

func readWorkCommandScenario(t *testing.T) workCommandScenario {
	t.Helper()
	raw, err := os.ReadFile("../m3/work-commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario workCommandScenario
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Goals) != 2 || scenario.QuietMS <= 0 {
		t.Fatalf("incomplete scenario: %+v", scenario)
	}
	return scenario
}

// TestM3WorkCommandsInstalledCore (m3/work-commands.json) sends the person's
// four choices through an installed Core's work capability. Each command is
// recorded in the ledger and applied on the next scheduling pass: ready steps
// follow the new rank, a paused goal starts no step until it is resumed, and a
// confirmed stop makes the goal stopped. Every change reaches the progress
// topic.
func TestM3WorkCommandsInstalledCore(t *testing.T) {
	t.Parallel()
	scenario := readWorkCommandScenario(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "system", "core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"work-commands-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeWorkLoad(t, root, 2)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestM3WorkCommandsChild$")
	child.Env = append(os.Environ(), workCommandsRoot+"="+root)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		_ = child.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = child.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = child.Process.Kill()
			<-done
		}
	})
	socket := filepath.Join(root, "run/client.sock")
	var c *clientapi.Client
	for attempt := 0; ; attempt++ {
		c, err = clientapi.DialWithCapabilities(ctx, socket, "work-commands", []protocol.Capability{{Name: "work", Version: "1.0"}, {Name: "progress", Version: "1.0"}})
		if err == nil {
			break
		}
		if ctx.Err() != nil || attempt > 1500 {
			t.Fatalf("Core socket: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer c.Close()
	reads := make(chan workCommandRead)
	go func() {
		for {
			envelope, err := c.Receive(ctx)
			select {
			case reads <- workCommandRead{envelope, err}:
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
			return protocol.Envelope{}, errWorkCommandWait
		case <-ctx.Done():
			return protocol.Envelope{}, ctx.Err()
		}
	}
	goalRows := func() []protocol.ProgressRow {
		t.Helper()
		view := "overview"
		if _, err := c.Progress(ctx, protocol.ProgressGet{View: &view}); err != nil {
			t.Fatalf("progress request: %v", err)
		}
		for {
			frame, readErr := next(15 * time.Second)
			if readErr != nil {
				t.Fatalf("progress read: %v", readErr)
			}
			if p, ok := frame.Payload.(protocol.Progress); ok && p.Rows != nil {
				return *p.Rows
			}
		}
	}
	first, second := scenario.Goals[0], scenario.Goals[1]
	// Both goal lines appear once the child has admitted them.
	var rows []protocol.ProgressRow
	for {
		if rows = goalRows(); len(rows) == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("goal lines never appeared: %+v", rows)
		case <-time.After(20 * time.Millisecond):
		}
	}
	topic := "progress"
	if _, err = c.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if frame, readErr := next(15 * time.Second); readErr != nil || frame.Error != nil {
		t.Fatalf("progress snapshot: %+v %v", frame, readErr)
	}
	// Core refuses mutations with unavailable while it recovers. That refusal is
	// proven before delivery, so the choice is sent again then. A reply is read
	// before any resend, so a stream frame can never duplicate a command.
	control := func(goal, operation string, confirm *bool) protocol.WorkControlResult {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := c.WorkControl(ctx, protocol.WorkControlRequest{GoalID: goal, Operation: operation, Confirm: confirm}); err != nil {
				t.Fatalf("%s %s: %v", operation, goal, err)
			}
			retry := false
			for !retry {
				frame, readErr := next(15 * time.Second)
				if readErr != nil {
					t.Fatalf("%s reply: %v", operation, readErr)
				}
				if frame.Error != nil {
					if frame.Error.Code != protocol.ErrorUnavailable || !time.Now().Before(deadline) {
						t.Fatalf("%s refused: %+v", operation, frame.Error)
					}
					retry = true
					continue
				}
				if result, ok := frame.Payload.(protocol.WorkControlResult); ok {
					return result
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	awaitRows := func(want func([]protocol.ProgressRow) bool) []protocol.ProgressRow {
		t.Helper()
		for {
			frame, readErr := next(15 * time.Second)
			if readErr != nil {
				t.Fatalf("progress update: %v", readErr)
			}
			if frame.Error != nil {
				t.Fatalf("progress update refused: %+v", frame.Error)
			}
			update, ok := frame.Payload.(protocol.ProgressUpdate)
			if !ok || update.Rows == nil {
				continue
			}
			if want(*update.Rows) {
				return *update.Rows
			}
		}
	}
	ordered := func(rows []protocol.ProgressRow, ids ...string) bool {
		if len(rows) != len(ids) {
			return false
		}
		for i, id := range ids {
			if rows[i].Ref != id {
				return false
			}
		}
		return true
	}
	lineFor := func(rows []protocol.ProgressRow, id string) *protocol.ProgressLine {
		for _, row := range rows {
			if row.Ref == id {
				line := row.Line
				return &line
			}
		}
		return nil
	}
	// Rank: the moved goal takes its neighbour's rank on the next pass, and the
	// progress topic publishes the new order.
	moved := control(first.ID, "rank_down", nil)
	if moved.Rank != "2" || moved.Operation != "rank_down" || moved.State != string(work.GoalWaiting) {
		t.Fatalf("rank_down result = %+v", moved)
	}
	awaitRows(func(rows []protocol.ProgressRow) bool { return ordered(rows, second.ID, first.ID) })
	// One free slot: only the higher-ranked goal starts a step.
	writeWorkLoad(t, root, 1)
	started := awaitWorkStarts(t, ctx, root, 1)
	if started[0] != second.Step {
		t.Fatalf("started %q with one slot, want the higher-ranked goal's step %q", started[0], second.Step)
	}
	// Pause: the waiting goal gets no step even when another slot frees up.
	if paused := control(first.ID, "pause", nil); !paused.Paused || paused.State != string(work.GoalWaiting) {
		t.Fatalf("pause result = %+v", paused)
	}
	rows = awaitRows(func(rows []protocol.ProgressRow) bool {
		line := lineFor(rows, first.ID)
		return line != nil && line.Now != nil && *line.Now == "Paused"
	})
	if line := lineFor(rows, first.ID); line == nil || line.Now == nil || *line.Now != "Paused" {
		t.Fatalf("paused line = %+v", rows)
	}
	// A repeated choice changes nothing and records no second command.
	if _, effect := controlEffect(t, ctx, c, next, first.ID, "pause", nil); effect != protocol.EffectNone {
		t.Fatalf("repeated pause effect = %s, want none", effect)
	}
	writeWorkLoad(t, root, 0)
	select {
	case <-time.After(time.Duration(scenario.QuietMS) * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("paused goal deadline")
	}
	if got := workStarts(t, root); len(got) != 1 {
		t.Fatalf("paused goal derived a step: %v", got)
	}
	if actions := goalActions(t, root, first.ID); actions != 0 {
		t.Fatalf("paused goal committed %d actions", actions)
	}
	// Resume: the goal returns to its rank and its step starts on the next pass.
	if resumed := control(first.ID, "resume", nil); resumed.Paused || resumed.State != string(work.GoalWaiting) {
		t.Fatalf("resume result = %+v", resumed)
	}
	started = awaitWorkStarts(t, ctx, root, 2)
	if started[1] != first.Step {
		t.Fatalf("resumed goal started %q, want %q", started[1], first.Step)
	}
	// Stop: confirmation is required, the goal becomes stopped and leaves the
	// progress lines. Each committed choice is one ledger command.
	confirm := true
	stoppedGoal := control(second.ID, "stop", &confirm)
	if stoppedGoal.State != string(work.GoalStopped) || stoppedGoal.Paused {
		t.Fatalf("stop result = %+v", stoppedGoal)
	}
	awaitRows(func(rows []protocol.ProgressRow) bool { return ordered(rows, first.ID) })
	if count := commandEventCount(t, root); count != 4 {
		t.Fatalf("ledger records %d commands, want the four choices: %v", count, commandEvents(t, root))
	}
	if got := workStarts(t, root); len(got) != 2 {
		t.Fatalf("a stopped goal started a step: %v", got)
	}
	_ = child.Process.Signal(syscall.SIGTERM)
	stopped = true
	closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancelClose()
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("child Core exit: %v", err)
		}
	case <-closeCtx.Done():
		t.Fatal("child Core did not stop")
	}
}

// TestM3WorkCommandsChild runs the fixture Core for the command scenario. It
// reports capacity from a file this test's parent writes and appends every
// started task, so the parent observes placement without owning the engine.
func TestM3WorkCommandsChild(t *testing.T) {
	root := os.Getenv(workCommandsRoot)
	if root == "" {
		return
	}
	scenario := readWorkCommandScenario(t)
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	world := &workCommandWorld{root: root}
	var mu sync.Mutex
	load := 2.0
	hostFor := func(value float64) work.Node {
		return work.Node{ID: "work-commands-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, LoadCPU: value, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"stub": true}}
	}
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) {
			go func() {
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
					value, err := readWorkLoad(root)
					if err != nil {
						continue
					}
					mu.Lock()
					changed := value != load
					load = value
					mu.Unlock()
					if changed {
						_ = e.NodeChanged(ctx, hostFor(value))
					}
				}
			}()
			return world, nil
		},
		MainHost: func(context.Context) (work.Node, error) {
			mu.Lock()
			defer mu.Unlock()
			return hostFor(load), nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := progressLineAgent(ctx, root)
			if err != nil {
				return err
			}
			for _, goal := range scenario.Goals {
				g := work.Goal{ID: goal.ID, Person: person, Agent: agent, Conversation: "main:" + agent, Summary: goal.Summary, Done: goal.Summary + " is ready", NoServiceReason: "plan", Scope: work.GoalScope{Repositories: []string{"fixture"}, Includes: goal.Step}, Criteria: []work.Criterion{{ID: "outcome", Statement: goal.Summary + " is ready", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "acceptance", Target: "fixture"}}}, Tasks: []work.Task{{ID: goal.Step, Done: goal.Step, Repository: "fixture", Base: "dev", Area: goal.Step + ".md", Worker: "stub", Resources: work.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, Stage: work.TaskReady}}}
				if err := e.Admit(ctx, g); err != nil {
					return fmt.Errorf("admit %s: %w", goal.ID, err)
				}
			}
			return nil
		},
	}}
	if err := boot.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// workCommandWorld starts no real worker. Every assignment holds its slot and
// reports a running worker, so a landed step never hides a later placement.
type workCommandWorld struct{ root string }

func (w *workCommandWorld) Authorize(context.Context, work.Action) error { return nil }

func (w *workCommandWorld) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, errors.New("unexpected observation")
}

func (w *workCommandWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	if a.Kind != work.Assign {
		<-ctx.Done()
		return work.Observation{}, ctx.Err()
	}
	file, err := os.OpenFile(filepath.Join(w.root, "started.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return work.Observation{}, err
	}
	_, err = file.WriteString(a.TaskID + "\n")
	err = errors.Join(err, file.Close())
	if err != nil {
		return work.Observation{}, err
	}
	return work.Observation{Status: work.OutcomeRunning, Handle: "worker-" + a.TaskID}, nil
}

func writeWorkLoad(t *testing.T, root string, load int) {
	t.Helper()
	path := filepath.Join(root, "capacity")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(load)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func readWorkLoad(root string) (float64, error) {
	raw, err := os.ReadFile(filepath.Join(root, "capacity"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
}

// workStarts lists the task ids whose worker start was dispatched so far.
func workStarts(t *testing.T, root string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "started.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}

// awaitWorkStarts waits until the fixture dispatched count worker starts.
func awaitWorkStarts(t *testing.T, ctx context.Context, root string, count int) []string {
	t.Helper()
	for {
		if got := workStarts(t, root); len(got) >= count {
			return got
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waited for %d worker starts: %v", count, workStarts(t, root))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// goalActions counts the execution actions a goal holds in the ledger.
func goalActions(t *testing.T, root, goalID string) int {
	t.Helper()
	db, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRowContext(t.Context(), "SELECT count(*) FROM work_records WHERE kind='action' AND json_extract(body,'$.goal_id')=?", goalID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// controlEffect sends one choice and returns its reply's effect state.
func controlEffect(t *testing.T, ctx context.Context, c *clientapi.Client, next func(time.Duration) (protocol.Envelope, error), goal, operation string, confirm *bool) (protocol.WorkControlResult, protocol.EffectState) {
	t.Helper()
	if _, err := c.WorkControl(ctx, protocol.WorkControlRequest{GoalID: goal, Operation: operation, Confirm: confirm}); err != nil {
		t.Fatalf("%s %s: %v", operation, goal, err)
	}
	for {
		frame, err := next(15 * time.Second)
		if err != nil {
			t.Fatalf("%s reply: %v", operation, err)
		}
		if frame.Error != nil {
			t.Fatalf("%s refused: %+v", operation, frame.Error)
		}
		if result, ok := frame.Payload.(protocol.WorkControlResult); ok {
			return result, frame.EffectState
		}
	}
}

// commandEvents lists the committed command bodies in ledger order.
func commandEvents(t *testing.T, root string) []string {
	t.Helper()
	db, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), "SELECT id||' '||body FROM work_events WHERE kind='goal.command' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return out
}

// commandEventCount reads the committed command events from the work ledger.
func commandEventCount(t *testing.T, root string) int {
	t.Helper()
	db, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRowContext(t.Context(), "SELECT count(*) FROM work_events WHERE kind='goal.command'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

var errWorkCommandWait = errors.New("work command read timed out")

type workCommandRead struct {
	envelope protocol.Envelope
	err      error
}
