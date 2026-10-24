// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTimers(t *testing.T, root string, epoch uint64) (*Store, *canon.Store) {
	t.Helper()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return New(db), db
}
func timerFixture(id, kind string) Timer {
	return Timer{ID: id, Person: "person", Agent: "agent", Conversation: "main:agent", Schedule: Schedule{Cron: "* * * * *", Zone: "UTC"}, Action: Action{Kind: kind, Text: "Read the saved result", Record: "record"}}
}

func TestGoalWaitFiresOnce(t *testing.T) {
	t.Parallel()
	s, _ := openTimers(t, t.TempDir(), 1)
	at := instant("2026-01-01T12:00:00Z")
	tm := Timer{ID: "goal-wait:g:mail.changed", Person: "person", Agent: "agent", Conversation: "main:agent", Schedule: Schedule{At: at}, Action: Action{Kind: "goal_wait", Text: "mail.changed", Record: "g"}}
	if _, err := s.Create(t.Context(), tm, at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	fires, err := s.ClaimDue(t.Context(), at, "1")
	if err != nil || len(fires) != 1 || fires[0].Timer.Action.Kind != "goal_wait" {
		t.Fatalf("fires=%v err=%v", fires, err)
	}
	if err = s.Finish(t.Context(), fires[0]); err != nil {
		t.Fatal(err)
	}
	fires, err = s.ClaimDue(t.Context(), at.Add(time.Hour), "1")
	if err != nil || len(fires) != 0 {
		t.Fatalf("deadline fired again: %v %v", fires, err)
	}
}

func TestClaimAndSignature(t *testing.T) {
	s, _ := openTimers(t, t.TempDir(), 1)
	ctx := t.Context()
	at := instant("2026-01-01T12:00:00Z")
	tm := timerFixture("sensor", "sensor")
	if _, err := s.Create(ctx, tm, at); err != nil {
		t.Fatal(err)
	}
	fires, err := s.ClaimDue(ctx, at.Add(60e9), "1")
	if err != nil || len(fires) != 1 {
		t.Fatalf("claims=%v err=%v", fires, err)
	}
	changed, err := s.Observe(ctx, fires[0], "revision-a")
	if err != nil || !changed {
		t.Fatalf("first=%v %v", changed, err)
	}
	if err = s.Finish(ctx, fires[0]); err != nil {
		t.Fatal(err)
	}
	fires, err = s.ClaimDue(ctx, at.Add(120e9), "1")
	if err != nil || len(fires) != 1 {
		t.Fatalf("claims=%v err=%v", fires, err)
	}
	changed, err = s.Observe(ctx, fires[0], "revision-a")
	if err != nil || changed {
		t.Fatalf("unchanged=%v %v", changed, err)
	}
	if err = s.Finish(ctx, fires[0]); err != nil {
		t.Fatal(err)
	}
	fires, err = s.ClaimDue(ctx, at.Add(120e9), "1")
	if err != nil || len(fires) != 0 {
		t.Fatalf("duplicate=%v %v", fires, err)
	}
}
func TestRecoverySafety(t *testing.T) {
	s, _ := openTimers(t, t.TempDir(), 1)
	ctx := t.Context()
	at := instant("2026-01-01T12:00:00Z")
	for _, kind := range []string{"reminder", "sensor", "job"} {
		if _, err := s.Create(ctx, timerFixture(kind, kind), at); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ClaimDue(ctx, at.Add(60e9), "1")
	if err != nil || len(first) != 3 {
		t.Fatalf("first=%v %v", first, err)
	}
	if err = s.Recover(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimDue(ctx, at.Add(60e9), "2")
	if err != nil || len(second) != 2 {
		t.Fatalf("safe recovery=%v %v", second, err)
	}
	for _, fire := range second {
		if fire.Timer.Action.Kind == "job" {
			t.Fatal("repeated effect-capable job")
		}
	}
	if err = s.Finish(ctx, first[0]); err == nil {
		t.Fatal("old claim acknowledged a replacement")
	}
	if err = s.Recover(ctx, "3"); err != nil {
		t.Fatal(err)
	}
	third, err := s.ClaimDue(ctx, at.Add(60e9), "3")
	if err != nil || len(third) != 0 {
		t.Fatalf("unbounded replacement=%v %v", third, err)
	}
}

func TestAuthCancellationAndAdmissionReplay(t *testing.T) {
	s, _ := openTimers(t, t.TempDir(), 1)
	ctx := t.Context()
	at := instant("2026-01-01T12:00:00Z")
	tm := timerFixture("auth", "reminder")
	if _, err := s.Create(ctx, tm, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, tm, at.Add(60e9)); err != nil {
		t.Fatal("identical replay", err)
	}
	changed := tm
	changed.Action.Text = "changed"
	if _, err := s.Create(ctx, changed, at); !errors.Is(err, ErrConflict) {
		t.Fatal("overwrote authored request", err)
	}
	if _, err := s.Get(ctx, tm.ID, "another-person", tm.Agent, tm.Conversation); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign timer read", err)
	}
	if err := s.Cancel(ctx, tm.ID, tm.Person, "another-agent", tm.Conversation); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign cancel", err)
	}
	if err := s.Cancel(ctx, tm.ID, tm.Person, tm.Agent, tm.Conversation); err != nil {
		t.Fatal(err)
	}
	fires, err := s.ClaimDue(ctx, at.Add(60e9), "1")
	if err != nil || len(fires) != 0 {
		t.Fatalf("cancelled fire=%+v %v", fires, err)
	}
}

func TestObservationSurvivesInterruptedDelivery(t *testing.T) {
	s, _ := openTimers(t, t.TempDir(), 1)
	ctx := t.Context()
	at := instant("2026-01-01T12:00:00Z")
	if _, err := s.Create(ctx, timerFixture("sensor", "sensor"), at); err != nil {
		t.Fatal(err)
	}
	fires, err := s.ClaimDue(ctx, at.Add(60e9), "1")
	if err != nil || len(fires) != 1 {
		t.Fatal(fires, err)
	}
	if changed, err := s.Observe(ctx, fires[0], "first"); err != nil || !changed {
		t.Fatal(changed, err)
	}
	// Even overdue occurrences must not overtake an unacknowledged sample.
	if later, err := s.ClaimDue(ctx, at.Add(120e9), "1"); err != nil || len(later) != 0 {
		t.Fatal(later, err)
	}
	if err = s.Recover(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	fires, err = s.ClaimDue(ctx, at.Add(60e9), "2")
	if err != nil || len(fires) != 1 {
		t.Fatal(fires, err)
	}
	if changed, err := s.Observe(ctx, fires[0], "first"); err != nil || !changed {
		t.Fatal("lost changed notification", changed, err)
	}
}

func TestExpiredRecurrenceAdmissionReplay(t *testing.T) {
	s, _ := openTimers(t, t.TempDir(), 1)
	at := instant("2026-01-01T12:00:00Z")
	tm := timerFixture("finite", "reminder")
	tm.Schedule = Schedule{Start: at, Zone: "UTC", RRule: "FREQ=DAILY;COUNT=1"}
	if _, err := s.Create(t.Context(), tm, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), tm, at.Add(24*time.Hour)); err != nil {
		t.Fatal("retry lost saved admission after recurrence ended", err)
	}
}

func TestConcurrentDueClaimsAndDST(t *testing.T) {
	for _, date := range []struct{ after, want, cron string }{
		{"2026-03-08T06:59:59Z", "2026-03-08T07:00:00Z", "30 2 * * *"},
		{"2026-11-01T04:59:59Z", "2026-11-01T05:30:00Z", "30 1 * * *"},
	} {
		t.Run(date.after, func(t *testing.T) {
			s, _ := openTimers(t, t.TempDir(), 1)
			tm := timerFixture("dst", "reminder")
			tm.Schedule = Schedule{Cron: date.cron, Zone: "America/New_York"}
			if _, err := s.Create(t.Context(), tm, instant(date.after)); err != nil {
				t.Fatal(err)
			}
			out := make(chan []Fire, 8)
			errs := make(chan error, 8)
			for range 8 {
				go func() { fires, err := s.ClaimDue(t.Context(), instant(date.want), "1"); out <- fires; errs <- err }()
			}
			var all []Fire
			for range 8 {
				all = append(all, (<-out)...)
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			if len(all) != 1 || !all[0].Instant.Equal(instant(date.want)) {
				t.Fatalf("duplicate fire: %+v", all)
			}
			if err := s.Finish(t.Context(), all[0]); err != nil {
				t.Fatal(err)
			}
			again, err := s.ClaimDue(t.Context(), instant(date.want).Add(time.Hour), "1")
			if err != nil || len(again) != 0 {
				t.Fatalf("repeated local hour: %+v %v", again, err)
			}
		})
	}
}

func TestFailedClaimFencingAndCancellation(t *testing.T) {
	s, _ := openTimers(t, t.TempDir(), 1)
	ctx := t.Context()
	at := instant("2026-01-01T12:00:00Z")
	tm := timerFixture("failure", "sensor")
	if _, err := s.Create(ctx, tm, at); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimDue(ctx, at.Add(time.Minute), "1")
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	if changed, err := s.Observe(ctx, first[0], "revision"); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if err := s.Fail(ctx, first[0]); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimDue(ctx, at.Add(time.Minute), "1")
	if err != nil || len(second) != 1 || second[0].ID != first[0].ID || second[0].Attempt != 2 {
		t.Fatal(second, err)
	}
	if err := s.Fail(ctx, first[0]); !errors.Is(err, ErrClaim) {
		t.Fatalf("stale failure changed replacement: %v", err)
	}
	if err := s.Finish(ctx, first[0]); !errors.Is(err, ErrClaim) {
		t.Fatalf("stale acknowledgement changed replacement: %v", err)
	}
	if changed, err := s.Observe(ctx, second[0], "revision"); err != nil || !changed {
		t.Fatal("lost frozen observation", changed, err)
	}
	if err := s.Recover(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	if third, err := s.ClaimDue(ctx, at.Add(time.Minute), "2"); err != nil || len(third) != 0 {
		t.Fatalf("restart renewed retry budget: %v %v", third, err)
	}

	tm = timerFixture("cancelled-failure", "reminder")
	if _, err := s.Create(ctx, tm, at); err != nil {
		t.Fatal(err)
	}
	fires, err := s.ClaimDue(ctx, at.Add(time.Minute), "2")
	if err != nil || len(fires) != 1 {
		t.Fatal(fires, err)
	}
	if err := s.Cancel(ctx, tm.ID, tm.Person, tm.Agent, tm.Conversation); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(ctx, fires[0]); err != nil {
		t.Fatal(err)
	}
	status, err := s.Get(ctx, tm.ID, tm.Person, tm.Agent, tm.Conversation)
	if err != nil || len(status.Fires) != 1 || status.Fires[0].State != "cancelled" {
		t.Fatal(status, err)
	}
	if retry, err := s.ClaimDue(ctx, at.Add(time.Minute), "2"); err != nil || len(retry) != 0 {
		t.Fatal("cancelled fire retried", retry, err)
	}
}
