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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

// A run limit is one of the person's choices, so Core grants it only to an
// eligible decider, a process outside Core's own tree. The fixture therefore
// runs Core in a child process and sends the choices from this test.
const runLimitRoot = "LINA_RUN_LIMIT_ROOT"

type runLimitScenario struct {
	Goals []struct {
		ID, Summary, Step string
	} `json:"goals"`
	QuietMS int64 `json:"quiet_ms"`
}

func readRunLimitScenario(t *testing.T) runLimitScenario {
	t.Helper()
	raw, err := os.ReadFile("../m3/run-limit.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario runLimitScenario
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Goals) != 3 || scenario.QuietMS <= 0 {
		t.Fatalf("incomplete scenario: %+v", scenario)
	}
	return scenario
}

// TestM3RunLimitInstalledCore (m3/run-limit.json) sends the run limit choices
// through an installed Core. A goal past the limit waits with its place and one
// reason, a lower limit stops nothing that runs, and the automatic limit follows
// what the host reports.
func TestM3RunLimitInstalledCore(t *testing.T) {
	t.Parallel()
	scenario := readRunLimitScenario(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "system", "core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"run-limit-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeWorkLoad(t, root, 0)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestM3RunLimitChild$")
	child.Env = append(os.Environ(), runLimitRoot+"="+root)
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
		c, err = clientapi.DialWithCapabilities(ctx, socket, "run-limit", []protocol.Capability{{Name: "work", Version: "1.0"}, {Name: "progress", Version: "1.0"}})
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
	progressRows := func() []protocol.ProgressRow {
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
	awaitNow := func(id, want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		seen := ""
		for {
			rows := progressRows()
			for _, row := range rows {
				if row.Ref != id {
					continue
				}
				now := ""
				if row.Line.Now != nil {
					now = *row.Line.Now
				}
				if now == want {
					return
				}
				seen = now
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never showed %q, last %q", id, want, seen)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Core refuses mutations with unavailable while it recovers. A reply is read
	// before any resend, so a stream frame can never duplicate a choice.
	limit := func(operation string, value *json.Number) protocol.WorkLimitResult {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := c.WorkLimit(ctx, protocol.WorkLimitRequest{Operation: operation, Value: value}); err != nil {
				t.Fatalf("%s: %v", operation, err)
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
				if result, ok := frame.Payload.(protocol.WorkLimitResult); ok {
					return result
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	alpha, beta, gamma := scenario.Goals[0], scenario.Goals[1], scenario.Goals[2]
	// The fixture starts with the person's limit at one and a host that carries
	// three steps. Only the first-ranked goal runs; the other two know their
	// place and the slot reason.
	for {
		if rows := progressRows(); len(rows) == 3 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("goal lines never appeared: %+v", progressRows())
		case <-time.After(20 * time.Millisecond):
		}
	}
	started := awaitWorkStarts(t, ctx, root, 1)
	if started[0] != alpha.Step {
		t.Fatalf("started %q, want the first-ranked step %q", started[0], alpha.Step)
	}
	awaitNow(beta.ID, "Waiting for a slot · #1")
	awaitNow(gamma.ID, "Waiting for a slot · #2")
	// A number the person sets lets the next goal in rank start on the next pass.
	two := json.Number("2")
	if result := limit("limit_set", &two); result.Automatic || result.Limit != "2" || result.Reason != protocol.RunLimitReasonSlot {
		t.Fatalf("limit_set = %+v", result)
	}
	started = awaitWorkStarts(t, ctx, root, 2)
	if started[1] != beta.Step {
		t.Fatalf("started %q, want %q", started[1], beta.Step)
	}
	awaitNow(gamma.ID, "Waiting for a slot · #1")
	// A lower limit stops nothing: both steps keep running and the last goal
	// still waits for its place.
	one := json.Number("1")
	if result := limit("limit_set", &one); result.Limit != "1" {
		t.Fatalf("limit_set one = %+v", result)
	}
	awaitNow(gamma.ID, "Waiting for a slot · #1")
	select {
	case <-time.After(time.Duration(scenario.QuietMS) * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("lower limit deadline")
	}
	if got := workStarts(t, root); len(got) != 2 {
		t.Fatalf("a lower limit started or stopped work: %v", got)
	}
	// Automatic on a loaded host: the limit falls to what the host carries and
	// the waiting goal's reason becomes the host load. Steps in progress run on.
	writeWorkLoad(t, root, 2)
	awaitHostLoad(t, ctx, root, 2)
	if result := limit("limit_auto", nil); !result.Automatic {
		t.Fatalf("limit_auto = %+v", result)
	}
	awaitNow(gamma.ID, "Waiting for host load · #1")
	awaitLimit(t, ctx, c, next, protocol.RunLimitState{Automatic: true, Value: "1", Reason: protocol.RunLimitReasonLoad})
	if got := workStarts(t, root); len(got) != 2 {
		t.Fatalf("the automatic limit stopped or started work: %v", got)
	}
	// The same automatic limit lets it start once the host is free again.
	writeWorkLoad(t, root, 0)
	awaitHostLoad(t, ctx, root, 0)
	started = awaitWorkStarts(t, ctx, root, 3)
	if started[2] != gamma.Step {
		t.Fatalf("started %q, want %q", started[2], gamma.Step)
	}
	// It holds a run slot now, so its place is gone and the line is no longer
	// a queue entry.
	awaitNow(gamma.ID, "Waiting")
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

// awaitHostLoad waits until Core has committed the fixture host's report, so a
// choice that follows reads the same capacity the person's line shows.
func awaitHostLoad(t *testing.T, ctx context.Context, root string, load int) {
	t.Helper()
	for {
		db, err := canon.OpenSQLiteReader(ctx, filepath.Join(root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		var committed float64
		if err = db.QueryRowContext(ctx, `SELECT coalesce(json_extract(body,'$.load_cpu'),-1) FROM work_records WHERE kind='node'`).Scan(&committed); err == nil && int(committed) == load {
			db.Close()
			return
		}
		db.Close()
		select {
		case <-ctx.Done():
			t.Fatalf("host load %d was never reported", load)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// awaitLimit waits for the progress payload to carry the applied limit.
func awaitLimit(t *testing.T, ctx context.Context, c *clientapi.Client, next func(time.Duration) (protocol.Envelope, error), want protocol.RunLimitState) {
	t.Helper()
	for {
		view := "overview"
		if _, err := c.Progress(ctx, protocol.ProgressGet{View: &view}); err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := next(15 * time.Second)
			if err != nil {
				t.Fatalf("progress read: %v", err)
			}
			p, ok := frame.Payload.(protocol.Progress)
			if !ok {
				continue
			}
			if p.Limit != nil && *p.Limit == want {
				return
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("limit never became %+v", want)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestM3RunLimitChild runs the fixture Core for the run limit scenario. It
// reports capacity from a file this test's parent writes and appends every
// started task, so the parent observes placement without owning the engine.
func TestM3RunLimitChild(t *testing.T) {
	root := os.Getenv(runLimitRoot)
	if root == "" {
		return
	}
	scenario := readRunLimitScenario(t)
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	world := &workCommandWorld{root: root}
	var mu sync.Mutex
	load := 0.0
	hostFor := func(value float64) goal.Node {
		return goal.Node{ID: "run-limit-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 3, CPULimit: 3, LoadCPU: value, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"stub": true}}
	}
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *goal.Engine) (goal.Driver, error) {
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
		MainHost: func(context.Context) (goal.Node, error) {
			mu.Lock()
			defer mu.Unlock()
			return hostFor(load), nil
		},
		Ready: func(ctx context.Context, e *goal.Engine, person, _ string) error {
			agent, err := progressLineAgent(ctx, root)
			if err != nil {
				return err
			}
			// The scenario begins with the person's own number already recorded,
			// so the queue exists before this fixture admits the goals.
			if _, _, err = e.SetRunLimit(ctx, person, goal.RunLimit{Value: 1}, "fixture-run-limit"); err != nil {
				return err
			}
			for _, g := range scenario.Goals {
				admit := goal.Goal{ID: g.ID, Person: person, Agent: agent, Conversation: "main:" + agent, Summary: g.Summary, Done: g.Summary + " is ready", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"fixture"}, Includes: g.Step}, Criteria: []goal.Criterion{{ID: "outcome", Statement: g.Summary + " is ready", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "fixture"}}}, Tasks: []goal.Task{{ID: g.Step, Done: g.Step, Repository: "fixture", Base: "dev", Area: g.Step + ".md", Worker: "stub", Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, Stage: goal.TaskReady}}}
				if err := e.Admit(ctx, admit); err != nil {
					return fmt.Errorf("admit %s: %w", g.ID, err)
				}
			}
			return nil
		},
	}}
	if err := boot.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
