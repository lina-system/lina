// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestDispatchPinsAdmissionBeforeManagerAndResumesAfterRestart(t *testing.T) {
	db := store(t)
	d := definition(t)
	base := gitFixture(t, d.Repository, "rev-parse", "dev")
	catalog, err := skills.Open(t.TempDir(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	method, err := catalog.Patch(t.Context(), skills.Patch{ID: "custom-method", Files: skills.Bundle{"SKILL.md": {Text: "---\nname: custom-method\ndescription: Synthetic procedure.\n---\nInspect the fixture.\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	d.Skill = "custom-method"
	origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"}
	call := provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(mustJSON(d))}
	var pinned Record
	s, err := openService(t, Config{DB: db, Epoch: 1, Skills: catalog, Manage: func(ctx context.Context, r Record) (Record, error) {
		reader := &Service{cfg: Config{DB: db}}
		saved, err := reader.GoalRecord(ctx, origin, call.ID)
		if err != nil || saved.State != "admitting" || saved.BaseCommit != base || !reflect.DeepEqual(saved, r) {
			t.Errorf("manager ran without a durable pinned admission: state=%s base=%s err=%v", saved.State, saved.BaseCommit, err)
		}
		pinned = r
		// Simulate interruption after the manager's independent durable decision.
		return r, context.Canceled
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if got := s.Tool(origin).Execute(t.Context(), call); got.Code != loop.Denied {
		t.Fatalf("interrupted manager returned a receipt: %+v", got)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GoalRecord(t.Context(), origin, call.ID)
	if err != nil || saved.State != "admitting" || !reflect.DeepEqual(saved, pinned) {
		t.Fatalf("interruption lost pending admission: %+v err=%v", saved, err)
	}
	if claimed, err := s.claim(t.Context(), &saved); err != nil || claimed {
		t.Fatalf("pending admission became runnable: claimed=%v err=%v", claimed, err)
	}
	if err := os.WriteFile(filepath.Join(d.Repository, "design.md"), []byte("# Changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, d.Repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-am", "Move base")
	if _, err := catalog.Undo(t.Context(), method.Undo); err != nil {
		t.Fatal(err)
	}
	managed := 0
	s, err = openService(t, Config{DB: db, Epoch: 1, Skills: catalog, Manage: func(_ context.Context, r Record) (Record, error) {
		managed++
		if !reflect.DeepEqual(r, pinned) {
			t.Errorf("retry changed the manager's snapshot: %+v", r)
		}
		r.Owner, r.Manager = "dev-owner", "manager:original"
		return r, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	tool := s.Tool(origin)
	altered := d
	altered.Done = "different task"
	if got := tool.Execute(t.Context(), provider.ToolCall{ID: call.ID, Name: call.Name, Arguments: string(mustJSON(altered))}); got.Code != loop.Denied {
		t.Fatal("pending admission accepted a changed definition", got)
	}
	foreign := origin
	foreign.Agent = "other-agent"
	if got := s.Tool(foreign).Execute(t.Context(), call); got.Code != loop.Denied {
		t.Fatal("pending admission accepted a changed origin", got)
	}
	if _, err := tool.Validate(call); err != nil {
		t.Fatal("pending admission lost its removed method", err)
	}
	first := tool.Execute(t.Context(), call)
	if first.Code != loop.OK || managed != 1 {
		t.Fatalf("pending admission did not resume manager: %+v calls=%d", first, managed)
	}
	saved, err = s.GoalRecord(t.Context(), origin, call.ID)
	pinned.State, pinned.Owner, pinned.Manager = "queued", "dev-owner", "manager:original"
	if err != nil || !reflect.DeepEqual(saved, pinned) {
		t.Fatalf("resumed admission changed snapshot: %+v err=%v", saved, err)
	}
	if again := tool.Execute(t.Context(), call); again.Code != loop.OK || string(again.Output) != string(first.Output) || managed != 1 {
		t.Fatalf("queued retry reran manager: %+v calls=%d", again, managed)
	}
}

func TestDispatchConcurrentRetryKeepsPinnedBaseAndLaterState(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	db := store(t)
	d := definition(t)
	base := gitFixture(t, d.Repository, "rev-parse", "dev")
	origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"}
	call := provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(mustJSON(d))}
	entered, release := make(chan struct{}), make(chan struct{})
	var manages atomic.Int32
	s, err := openService(t, Config{DB: db, Epoch: 1, Manage: func(ctx context.Context, r Record) (Record, error) {
		if r.BaseCommit != base {
			t.Errorf("concurrent manager received a different base: %s", r.BaseCommit)
		}
		if manages.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return r, ctx.Err()
			}
			r.Owner, r.Manager = "late-owner", "manager:late"
		} else {
			r.Owner, r.Manager = "dev-owner", "manager:original"
		}
		return r, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	tool := s.Tool(origin)
	first := make(chan loop.Result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		first <- tool.Execute(ctx, call)
	}()
	t.Cleanup(func() { cancel(); <-finished })
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	gitFixture(t, d.Repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "Move base")
	second := tool.Execute(ctx, call)
	if second.Code != loop.OK {
		t.Fatal(second)
	}
	saved, err := s.GoalRecord(ctx, origin, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved.State, saved.Code = "finished", "ready_for_review"
	if err := s.save(ctx, saved); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case result := <-first:
		if result.Code != loop.OK || string(result.Output) != string(second.Output) {
			t.Fatalf("late manager replaced the admitted receipt: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	got, err := s.GoalRecord(ctx, origin, call.ID)
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("late manager reset execution state: %+v err=%v", got, err)
	}
}

func TestGoalRetriesIncompleteManagerAdmission(t *testing.T) {
	db := store(t)
	d := definition(t)
	base := gitFixture(t, d.Repository, "rev-parse", "dev")
	origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:goal"}
	manages := 0
	s, err := openService(t, Config{DB: db, Epoch: 1, Manage: func(_ context.Context, r Record) (Record, error) {
		manages++
		if r.BaseCommit != base {
			t.Errorf("goal retry lost pinned base: %s", r.BaseCommit)
		}
		return r, context.Canceled
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := s.RunGoal(t.Context(), origin, "action", d); err == nil || manages != attempt {
			t.Fatalf("goal skipped incomplete manager admission: calls=%d err=%v", manages, err)
		}
		gitFixture(t, d.Repository, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "Move base")
	}
	saved, err := s.GoalRecord(t.Context(), origin, "action")
	if err != nil || saved.State != "admitting" || saved.Goal || saved.BaseCommit != base {
		t.Fatalf("incomplete goal admission became runnable: %+v err=%v", saved, err)
	}
}
