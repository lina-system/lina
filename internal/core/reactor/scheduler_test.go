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

func schedulerFixture(t *testing.T) *work.Service {
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
func scheduledTask(id string, deps ...string) work.Task {
	return work.Task{ID: id, AgentID: "agent", ConversationID: "conversation", Title: id, Done: "checked", Schedule: &work.Schedule{Group: "g", Repository: "owner/repo", Worker: "worker", Host: "local", Areas: []string{id + "/**"}, DependsOn: deps}}
}
func TestEventsDispatchAndMergeReadRelease(t *testing.T) {
	s := schedulerFixture(t)
	starts := []string{}
	reads := 0
	returns := 0
	merged := false
	r := NewScheduler(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
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
	if err := r.Schedule(t.Context(), []work.Task{scheduledTask("a"), scheduledTask("b", "a"), scheduledTask("c")}); err != nil {
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
	s := schedulerFixture(t)
	attempts := 0
	r := NewScheduler(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
		return work.Availability{Hosts: map[string]int{"local": 2}}, nil
	}, Start: func(context.Context, work.Card) (work.Run, error) {
		attempts++
		return work.Run{}, errors.New("connection lost")
	}})
	if err := r.Schedule(t.Context(), []work.Task{scheduledTask("a")}); err == nil {
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

func TestKnownPreDispatchFailureReleasesReservations(t *testing.T) {
	s := schedulerFixture(t)
	starts := map[string]int{}
	a, b, dependent := scheduledTask("a"), scheduledTask("b"), scheduledTask("dependent", "a")
	b.Schedule.Group = "other"
	b.Schedule.Areas = a.Schedule.Areas
	r := NewScheduler(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
		return work.Availability{Hosts: map[string]int{"local": 1}}, nil
	}, Start: func(_ context.Context, c work.Card) (work.Run, error) {
		starts[c.Task.ID]++
		if c.Task.ID == "a" {
			return work.Run{}, errors.Join(ErrNotStarted, errors.New("fetch failed"))
		}
		return work.Run{ID: "run:" + c.Task.ID, TaskID: c.Task.ID, Worker: "worker", NodeID: "local", ThreadID: "thread:" + c.Task.ID, TurnID: "turn", Generation: 1}, nil
	}})
	if err := r.Schedule(t.Context(), []work.Task{a, b, dependent}); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("known failure hidden: %v", err)
	}
	failed, err := s.Get(t.Context(), "a")
	if err != nil || failed.Run != nil || failed.Scheduling.AreaHeld || failed.Scheduling.CapacityHeld || failed.State != "failed" {
		t.Fatalf("pre-dispatch failure kept reservations: %+v %v", failed, err)
	}
	if starts["b"] != 1 || starts["dependent"] != 0 {
		t.Fatalf("released work: %v", starts)
	}
	if err := s.Recover(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	if err := r.Wake(t.Context()); err != nil {
		t.Fatal(err)
	}
	failed, err = s.Get(t.Context(), "a")
	if err != nil || starts["a"] != 1 || starts["b"] != 1 || starts["dependent"] != 0 || failed.Scheduling.AreaHeld || failed.Scheduling.CapacityHeld || failed.State != "failed" {
		t.Fatalf("recovery replayed or lost failure: starts=%v failed=%+v err=%v", starts, failed, err)
	}
}

func TestImmediateCompletionWaitsForAdmission(t *testing.T) {
	s := schedulerFixture(t)
	completed := make(chan error, 1)
	entered := make(chan struct{})
	var r *Scheduler
	r = NewScheduler(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
		return work.Availability{Hosts: map[string]int{"local": 1}}, nil
	}, Start: func(ctx context.Context, c work.Card) (work.Run, error) {
		go func() { close(entered); completed <- r.Completed(ctx, c.Task.ID, "thread", "turn", "completed") }()
		<-entered
		return work.Run{ID: "run", TaskID: c.Task.ID, Worker: "worker", NodeID: "local", ThreadID: "thread", TurnID: "turn", Generation: 1}, nil
	}})
	if err := r.Schedule(t.Context(), []work.Task{scheduledTask("a")}); err != nil {
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
	s := schedulerFixture(t)
	returns := 0
	conflict := false
	r := NewScheduler(s, Hooks{Availability: func(context.Context) (work.Availability, error) {
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
	if err := r.Schedule(t.Context(), []work.Task{scheduledTask("a"), scheduledTask("b")}); err != nil {
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
