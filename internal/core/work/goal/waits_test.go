// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func outsideWait() Wait {
	return Wait{
		Reason:         "reply from the host",
		Wake:           "mail.changed",
		Until:          500,
		Recommendation: "no reply from the host yet; I recommend a short follow-up",
		Draft:          "Hello, checking whether Thursday still works.",
		TodoID:         "host-reply",
		Who:            "host",
		Since:          "2026-10-20",
		NextCheck:      "2026-10-23",
		Source:         "mail:thread-1",
	}
}

func boolPtr(v bool) *bool { return &v }

func recordOutsideWait(t *testing.T, e *Engine, w Wait) {
	t.Helper()
	if err := e.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Actions) != 1 || s.Actions[0].Kind != PlanGoal {
		t.Fatalf("manager turn: %+v", s.Actions)
	}
	if _, err = e.Claim(t.Context(), s.Actions[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), s.Actions[0].ID, e.cfg.Epoch, Observation{Status: OutcomeSucceeded, Wait: &w}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func sourceReads(s State) []Action {
	var out []Action
	for _, a := range s.Actions {
		if a.Kind == ReadSource {
			out = append(out, a)
		}
	}
	return out
}

func TestOutsideWaitIsNotReadOnAnInterval(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	recordOutsideWait(t, e, outsideWait())
	for range 3 {
		if err = e.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Goals[0].State != GoalWaiting || len(sourceReads(s)) != 0 {
		t.Fatalf("interval read: state %s reads %+v", s.Goals[0].State, sourceReads(s))
	}
	w := s.Goals[0].Waits[0]
	if w.TodoID != "host-reply" || w.Who != "host" || w.Since != "2026-10-20" || w.NextCheck != "2026-10-23" || w.Until != 500 {
		t.Fatalf("waiting_on link: %+v", w)
	}
}

func TestChangeSignalReadsTheSourceOnce(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	recordOutsideWait(t, e, outsideWait())
	signal := Signal{ID: "mail-1", GoalID: "g", Wake: "mail.changed"}
	if err = e.Signal(t.Context(), signal); err != nil {
		t.Fatal(err)
	}
	if err = e.Signal(t.Context(), signal); err != nil {
		t.Fatal(err)
	}
	if err = e.Signal(t.Context(), Signal{ID: "mail-2", GoalID: "g", Wake: "mail.changed"}); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reads := sourceReads(s)
	if len(reads) != 1 || reads[0].State != "queued" || reads[0].ReadCause != "signal" || reads[0].WaitSource != "mail:thread-1" {
		t.Fatalf("signal reads: %+v", reads)
	}
	if _, err = e.Claim(t.Context(), reads[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), reads[0].ID, 1, Observation{Status: OutcomeSucceeded, SourceOpen: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reads = sourceReads(s)
	open := 0
	for _, a := range reads {
		if a.State != "finished" {
			open++
		}
	}
	if len(reads) != 2 || open != 1 {
		t.Fatalf("follow-up signal did not stay one read: %+v", reads)
	}
	var follow Action
	for _, a := range reads {
		if a.State != "finished" {
			follow = a
		}
	}
	if _, err = e.Claim(t.Context(), follow.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), follow.ID, 1, Observation{Status: OutcomeSucceeded, SourceOpen: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Goals[0].Waits) != 0 || s.Goals[0].DecisionRow != "" || s.Goals[0].State == GoalBlocked {
		t.Fatalf("closed read did not end the wait: %+v", s.Goals[0])
	}
	for _, a := range s.Actions {
		if a.Kind != PlanGoal && a.Kind != ReadSource {
			t.Fatalf("read sent work: %+v", a)
		}
	}
}

func TestExpiryBecomesARowAndFiresOnceAfterDowntime(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	clock := time.Unix(100, 0)
	now := func() time.Time { return clock }
	var armed []Wait
	var linked []Wait
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now,
		ArmWait: func(_ context.Context, _ Goal, w Wait) error {
			armed = append(armed, w)
			return nil
		},
		LinkWaitingOn: func(_ context.Context, _ Goal, w Wait) error {
			linked = append(linked, w)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recordOutsideWait(t, e, outsideWait())
	if len(armed) != 1 || armed[0].Until != 500 || len(linked) != 1 || linked[0].TodoID != "host-reply" || linked[0].NextCheck != "2026-10-23" {
		t.Fatalf("timer or waiting_on: armed %+v linked %+v", armed, linked)
	}
	clock = time.Unix(500, 0)
	restart, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = restart.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = restart.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := restart.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reads := sourceReads(s)
	if len(reads) != 1 || reads[0].ReadCause != "deadline" || reads[0].State != "queued" {
		t.Fatalf("startup deadline read: %+v", reads)
	}
	if _, err = restart.Claim(t.Context(), reads[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = restart.Report(t.Context(), reads[0].ID, 2, Observation{Status: OutcomeSucceeded, SourceOpen: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if err = restart.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = restart.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := s.Goals[0]
	if g.State != GoalBlocked || len(g.Waits) != 0 || !strings.Contains(g.BlockedReason, outsideWait().Recommendation) || !strings.Contains(g.BlockedReason, outsideWait().Draft) || !strings.HasPrefix(g.BlockedReason, outsideWait().Recommendation) {
		t.Fatalf("expiry row: %+v", g)
	}
	for _, a := range s.Actions {
		if a.State != "finished" || a.Kind != PlanGoal && a.Kind != ReadSource {
			t.Fatalf("expiry sent or repeated work: %+v", a)
		}
	}
	attention := g.AttentionID
	again, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 3, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = again.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = again.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Goals[0].AttentionID != attention || s.Goals[0].BlockedReason != g.BlockedReason || len(sourceReads(s)) != 1 {
		t.Fatalf("restart repeated expiry: %+v reads %+v", s.Goals[0], sourceReads(s))
	}
}

func TestSiblingWorkContinuesAfterTheExpiryRow(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	clock := time.Unix(100, 0)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Claim(t.Context(), s.Actions[0].ID); err != nil {
		t.Fatal(err)
	}
	w := outsideWait()
	if err = e.Report(t.Context(), s.Actions[0].ID, 1, Observation{Status: OutcomeSucceeded, Wait: &w, Tasks: []Task{readyTask("notes")}}); err != nil {
		t.Fatal(err)
	}
	clock = time.Unix(500, 0)
	n := readyNode("n")
	n.ObservedAt = clock.Unix()
	if err = e.NodeChanged(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var read, assign Action
	for _, a := range s.Actions {
		switch {
		case a.Kind == ReadSource && a.State == "queued":
			read = a
		case a.Kind == Assign && a.TaskID == "notes" && a.State == "queued":
			assign = a
		}
	}
	if read.ID == "" || assign.ID == "" {
		t.Fatalf("deadline stopped the sibling: %+v", s.Actions)
	}
	if _, err = e.Claim(t.Context(), read.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), read.ID, 1, Observation{Status: OutcomeSucceeded, SourceOpen: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Goals[0].DecisionRow == "" || !strings.Contains(s.Goals[0].BlockedReason, w.Draft) {
		t.Fatalf("row missing while sibling runs: %+v", s.Goals[0])
	}
	if _, err = e.Claim(t.Context(), assign.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPartialExpiryActionIsRefused(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Claim(t.Context(), s.Actions[0].ID); err != nil {
		t.Fatal(err)
	}
	w := outsideWait()
	w.Draft = ""
	if err = e.Report(t.Context(), s.Actions[0].ID, 1, Observation{Status: OutcomeSucceeded, Wait: &w}); err == nil {
		t.Fatal("expiry without a draft was recorded")
	}
}
