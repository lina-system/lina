// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

func TestGoalWaitStubPlugin(t *testing.T) {
	if os.Getenv("LINA_GOAL_WAIT_STUB") != "1" {
		return
	}
	fmt.Println(os.Getenv("LINA_GOAL_WAIT_STUB_RESULT"))
}

// TestGoalWaitsThroughRunningCore is the headless outside-wait scenario.
// A stub plugin reads the source once per change signal. After the deadline
// passes while Core is down, startup reads it once more and, while it is still
// open, leaves a row with the recommendation and the draft. Nothing is sent.
func TestGoalWaitsThroughRunningCore(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-goal-waits-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-work-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	deadline := clock.Add(time.Hour)
	const (
		statement = "the host has confirmed Thursday"
		recommend = "no reply from the host yet; I recommend a short follow-up"
		draft     = "Hello, checking whether Thursday still works."
	)
	wait := work.Wait{
		Reason: recommend, Wake: "mail.changed", Until: deadline.Unix(),
		Recommendation: recommend, Draft: draft,
		TodoID: "host-reply", Who: "host", Since: clock.Format(time.DateOnly), NextCheck: deadline.Format(time.DateOnly),
		Source: "mail:thread-1",
	}
	world := &waitWorld{ready: make(chan struct{}), wake: make(chan struct{}, 1), result: "open", recorded: wait}
	var engine *work.Engine
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Now: func() time.Time { return clock },
		Driver: func(e *work.Engine) (work.Driver, error) {
			engine = e
			return world, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := goalLivenessOwner(ctx, root)
			if err != nil {
				return err
			}
			saved, err := e.Snapshot(ctx)
			if err != nil {
				return err
			}
			if len(saved.Goals) > 0 {
				return nil
			}
			return e.Admit(ctx, work.Goal{
				ID: "wait-goal", Person: person, Agent: agent, Summary: "confirm the meeting", Done: statement, NoServiceReason: "mail",
				Scope:    work.GoalScope{Repositories: []string{"fixture-repo"}, Includes: "the meeting"},
				Criteria: []work.Criterion{{ID: "host", Statement: statement, Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "acceptance", Target: "fixture-repo"}}},
			})
		},
		Changed: func(_ context.Context, s work.State) error { world.note(s); return nil },
	}}
	run := func() (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- boot.Run(ctx, cfg) }()
		return cancel, done
	}
	cancel, done := run()
	t.Cleanup(cancel)
	select {
	case <-world.ready:
	case err := <-done:
		t.Fatalf("Core stopped: %v", err)
	case <-time.After(45 * time.Second):
		cancel()
		<-done
		t.Fatal("wait was not recorded")
	}
	db, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var nextAt string
	if err = db.QueryRowContext(t.Context(), "SELECT next_at FROM timer WHERE id=?", "goal-wait:wait-goal:mail.changed").Scan(&nextAt); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if nextAt == "" {
		t.Fatal("deadline timer was not saved")
	}
	if engine == nil {
		t.Fatal("work engine was not wired")
	}
	for _, id := range []string{"mail-1", "mail-1", "mail-2"} {
		if err = engine.Signal(t.Context(), work.Signal{ID: id, GoalID: "wait-goal", Wake: "mail.changed"}); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, world, done, func() bool {
		return readCount(world.calls) == 2 && world.goal.State == work.GoalWaiting && len(world.goal.Waits) == 1
	})
	world.mu.Lock()
	if world.goal.State != work.GoalWaiting || len(world.goal.Waits) != 1 || world.goal.Waits[0].NextCheck != wait.NextCheck {
		goal := world.goal
		world.mu.Unlock()
		t.Fatalf("signal reads did not leave the wait open: %+v", goal)
	}
	world.mu.Unlock()
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	clock = deadline
	cancel, done = run()
	t.Cleanup(cancel)
	waitUntil(t, world, done, func() bool {
		return world.goal.State == work.GoalBlocked && strings.Contains(world.goal.BlockedReason, recommend) && strings.Contains(world.goal.BlockedReason, draft) && readCount(world.calls) == 3
	})
	world.mu.Lock()
	attention := world.goal.AttentionID
	reason := world.goal.BlockedReason
	world.mu.Unlock()
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	published := world.published()
	cancel, done = run()
	t.Cleanup(cancel)
	waitUntil(t, world, done, func() bool { return world.publishedCount > published })
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	world.mu.Lock()
	defer world.mu.Unlock()
	if readCount(world.calls) != 3 || world.goal.AttentionID != attention || world.goal.BlockedReason != reason {
		t.Fatalf("restart repeated the expiry: calls %d attention %s", readCount(world.calls), world.goal.AttentionID)
	}
	for _, a := range world.calls {
		if a.Kind != work.PlanGoal && a.Kind != work.ReadSource {
			t.Fatalf("wait sent work: %+v", a)
		}
	}
}

func readCount(calls []work.Action) int {
	n := 0
	for _, a := range calls {
		if a.Kind == work.ReadSource {
			n++
		}
	}
	return n
}

func waitUntil(t *testing.T, world *waitWorld, done <-chan error, ready func() bool) {
	t.Helper()
	deadline := time.After(45 * time.Second)
	for {
		world.mu.Lock()
		ok := ready()
		world.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-world.wake:
		case err := <-done:
			t.Fatalf("Core stopped: %v", err)
		case <-deadline:
			world.mu.Lock()
			defer world.mu.Unlock()
			t.Fatalf("wait scenario did not settle: goal %+v calls %d", world.goal, len(world.calls))
		}
	}
}

type waitWorld struct {
	mu             sync.Mutex
	calls          []work.Action
	goal           work.Goal
	recorded       work.Wait
	result         string
	publishedCount int
	opened         bool
	ready          chan struct{}
	wake           chan struct{}
}

func (w *waitWorld) published() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.publishedCount
}
func (w *waitWorld) Authorize(context.Context, work.Action) error { return nil }
func (w *waitWorld) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, fmt.Errorf("wait read was repeated")
}
func (w *waitWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	w.mu.Lock()
	w.calls = append(w.calls, a)
	result := w.result
	w.mu.Unlock()
	switch a.Kind {
	case work.PlanGoal:
		if a.GoalID != "wait-goal" {
			return work.Observation{}, fmt.Errorf("manager turn for %s", a.GoalID)
		}
		w.mu.Lock()
		recorded := w.recorded
		w.mu.Unlock()
		return work.Observation{Status: work.OutcomeSucceeded, Wait: &recorded}, nil
	case work.ReadSource:
		line, err := w.plugin(ctx, result)
		if err != nil {
			return work.Observation{}, err
		}
		open := line == "open"
		if line != "open" && line != "closed" {
			return work.Observation{}, fmt.Errorf("plugin said %q", line)
		}
		return work.Observation{Status: work.OutcomeSucceeded, SourceOpen: &open}, nil
	default:
		return work.Observation{}, fmt.Errorf("unexpected action %s", a.Kind)
	}
}

func (w *waitWorld) plugin(ctx context.Context, result string) (string, error) {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGoalWaitStubPlugin$")
	cmd.Env = []string{"LINA_GOAL_WAIT_STUB=1", "LINA_GOAL_WAIT_STUB_RESULT=" + result}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", err
	}
	_, _ = io.Copy(io.Discard, stdout)
	if err = input.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", err
	}
	if err = cmd.Wait(); err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (w *waitWorld) note(s work.State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.publishedCount++
	select {
	case w.wake <- struct{}{}:
	default:
	}
	for _, g := range s.Goals {
		if g.ID == "wait-goal" {
			w.goal = g
		}
	}
	if !w.opened && w.goal.State == work.GoalWaiting && len(w.goal.Waits) == 1 && w.goal.Waits[0].Wake == "mail.changed" && readCount(w.calls) == 0 {
		w.opened = true
		close(w.ready)
	}
}
