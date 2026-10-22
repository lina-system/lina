// SPDX-License-Identifier: Apache-2.0
package reactor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
)

func fixture(t *testing.T) *work.Service {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return work.New(db)
}
func task(id string, deps ...string) work.Task {
	return work.Task{ID: id, AgentID: "agent", ConversationID: "conversation", Title: id, Done: "checked", Schedule: &work.Schedule{Group: "g", Repository: "owner/repo", Worker: "worker", Host: "local", Areas: []string{id + "/**"}, DependsOn: deps}}
}
func TestEventsDispatchAndMergeReadRelease(t *testing.T) {
	s := fixture(t)
	starts := []string{}
	reads := 0
	returns := 0
	merged := false
	r := New(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
		return work.Availability{Hosts: map[string]int{"local": 5}}, nil
	},
		Start: func(_ context.Context, c work.Card) (work.Run, error) {
			starts = append(starts, c.Task.ID)
			return work.Run{ID: "run:" + c.Task.ID, TaskID: c.Task.ID, Worker: "worker", NodeID: "local", ThreadID: "thread:" + c.Task.ID, TurnID: "turn", Generation: 1}, nil
		},
		ReadPR: func(_ context.Context, c work.Card) (work.PullRequest, error) {
			reads++
			pr := work.PullRequest{Number: 1, Head: "head", State: "OPEN", Mergeable: "CONFLICTING"}
			if merged {
				pr.State = "MERGED"
				pr.MergeCommit = "merge"
			}
			return pr, nil
		},
		Return: func(_ context.Context, c work.Card) (work.Run, error) {
			returns++
			run := *c.Run
			run.ID += "-return"
			run.TurnID = "return"
			run.Generation++
			return run, nil
		},
	})
	if err := r.Schedule(t.Context(), []work.Task{task("a"), task("b", "a"), task("c")}); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 2 {
		t.Fatalf("starts %v", starts)
	}
	if err := r.Wake(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 2 {
		t.Fatal("duplicate dispatch")
	}
	if err := r.Finished(t.Context(), "a", "run:a", "completed"); err != nil {
		t.Fatal(err)
	}
	if returns != 1 || reads != 1 || len(starts) != 2 {
		t.Fatalf("return=%d reads=%d starts=%v", returns, reads, starts)
	}
	// A duplicated old completion cannot end the correction's new turn.
	if err := r.Finished(t.Context(), "a", "run:a", "completed"); !errors.Is(err, work.ErrState) {
		t.Fatalf("old completion %v", err)
	}
	merged = true
	if err := r.Finished(t.Context(), "a", "run:a-return", "completed"); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 3 || starts[2] != "b" || returns != 1 {
		t.Fatalf("release starts=%v returns=%d", starts, returns)
	}
}
func TestFailedForgeReadAndUnknownDispatchKeepLeases(t *testing.T) {
	s := fixture(t)
	attempts := 0
	r := New(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
		return work.Availability{Hosts: map[string]int{"local": 2}}, nil
	}, Start: func(context.Context, work.Card) (work.Run, error) {
		attempts++
		return work.Run{}, errors.New("connection lost")
	}})
	if err := r.Schedule(t.Context(), []work.Task{task("a")}); err == nil {
		t.Fatal("unknown start hidden")
	}
	if err := r.Wake(t.Context()); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatal("unknown start retried")
	}
	c, _ := s.Get(t.Context(), "a")
	if !c.Scheduling.AreaHeld || !c.Scheduling.CapacityHeld {
		t.Fatal("unknown outcome released")
	}
}

func TestImmediateCompletionWaitsForAdmission(t *testing.T) {
	s := fixture(t)
	completed := make(chan error, 1)
	entered := make(chan struct{})
	var r *Reactor
	r = New(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
		return work.Availability{Hosts: map[string]int{"local": 1}}, nil
	}, Start: func(ctx context.Context, c work.Card) (work.Run, error) {
		go func() { close(entered); completed <- r.Completed(ctx, c.Task.ID, "thread", "turn", "completed") }()
		<-entered
		return work.Run{ID: "run", TaskID: c.Task.ID, Worker: "worker", NodeID: "local", ThreadID: "thread", TurnID: "turn", Generation: 1}, nil
	}})
	if err := r.Schedule(t.Context(), []work.Task{task("a")}); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(t.Context(), "a")
	if c.Active || c.TurnStatus != "completed" {
		t.Fatalf("completion lost: %+v", c)
	}
}
func TestConflictReturnWaitsForCapacity(t *testing.T) {
	s := fixture(t)
	returns := 0
	conflict := false
	r := New(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
		return work.Availability{Hosts: map[string]int{"local": 1}}, nil
	},
		Start: func(_ context.Context, c work.Card) (work.Run, error) {
			return work.Run{ID: "run:" + c.Task.ID, TaskID: c.Task.ID, Worker: "worker", NodeID: "local", ThreadID: "thread:" + c.Task.ID, TurnID: "turn", Generation: 1}, nil
		},
		ReadPR: func(context.Context, work.Card) (work.PullRequest, error) {
			pr := work.PullRequest{Number: 1, Head: "head", State: "OPEN", Mergeable: "MERGEABLE"}
			if conflict {
				pr.Mergeable = "CONFLICTING"
			}
			return pr, nil
		},
		Return: func(_ context.Context, c work.Card) (work.Run, error) {
			returns++
			run := *c.Run
			run.ID += "-return"
			run.TurnID = "correction"
			run.Generation++
			return run, nil
		}})
	if err := r.Schedule(t.Context(), []work.Task{task("a"), task("b")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finished(t.Context(), "a", "run:a", "completed"); err != nil {
		t.Fatal(err)
	}
	conflict = true
	if err := r.Observe(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if returns != 0 {
		t.Fatal("conflict correction exceeded host capacity")
	}
	conflict = false
	if err := r.Finished(t.Context(), "b", "run:b", "completed"); err != nil {
		t.Fatal(err)
	}
	if returns != 1 {
		t.Fatalf("correction not released on capacity event: %d", returns)
	}
}
