// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestGrowthNextTurnUndoAndSuppression(t *testing.T) {
	s, db, root := setup(t)
	a, other := create(t, s, ""), create(t, s, "")
	ctx := t.Context()
	before, err := s.Prepare(ctx, "before", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := agents.State{Emotion: "fixture-emotion", Settings: map[string]string{"fixture": "value"}}
	if err := s.SetState(ctx, a.ID, state, "person"); err != nil {
		t.Fatal(err)
	}
	c := agents.GrowthCandidate{ID: "fixture-step", Expected: a.Revision, Snapshot: "fixture-growth-1", Notice: "fixture-learning-1"}
	step, err := s.AdoptGrowth(ctx, a.ID, "person", c)
	if err != nil || step.Revision != 2 || step.Notice == "" || strings.ContainsAny(step.Notice, "\r\n") {
		t.Fatalf("step=%+v err=%v", step, err)
	}
	if pinned, err := s.Prepare(ctx, "before", a.ID); err != nil || pinned != before {
		t.Fatalf("pinned=%+v %v", pinned, err)
	}
	next, err := s.Prepare(ctx, "after", a.ID)
	want := before.Layers
	want.GrowthSnapshot = c.Snapshot
	if err != nil || next.Revision != 2 || next.Layers != want || next.Commit == before.Commit {
		t.Fatalf("next=%+v %v", next, err)
	}
	// Retrying the committed candidate must not publish a second revision/notice.
	if replay, err := s.AdoptGrowth(ctx, a.ID, "person", c); err != nil || replay != step {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	// A subsequent non-growth edit must survive the one-action undo.
	want.UserVoice = "fixture-user-2"
	if _, err := s.Revise(ctx, a.ID, 2, want, "person"); err != nil {
		t.Fatal(err)
	}
	undone, err := s.UndoGrowth(ctx, a.ID, "person", step.Undo)
	if err != nil || undone.Revision != 4 {
		t.Fatalf("undo=%+v %v", undone, err)
	}
	want.GrowthSnapshot = before.Layers.GrowthSnapshot
	after, err := s.Prepare(ctx, "undone", a.ID)
	if err != nil || after.Layers != want || after.Commit == before.Commit {
		t.Fatalf("restored=%+v %v", after, err)
	}
	if pinned, err := s.Prepare(ctx, "after", a.ID); err != nil || pinned != next {
		t.Fatalf("undo changed prepared turn: %+v %v", pinned, err)
	}
	if replay, err := s.UndoGrowth(ctx, a.ID, "person", step.Undo); err != nil || replay.Revision != undone.Revision {
		t.Fatalf("undo replay=%+v %v", replay, err)
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, candidate := range []agents.GrowthCandidate{
		{ID: c.ID, Expected: 4, Snapshot: "changed-fixture", Notice: c.Notice},
		{ID: "renamed-fixture", Expected: 4, Snapshot: c.Snapshot, Notice: c.Notice},
	} {
		if out, err := reopened.AdoptGrowth(ctx, a.ID, "person", candidate); !errors.Is(err, agents.ErrGrowthUndone) || out.Notice != "" {
			t.Fatalf("undone reintroduced=%+v %v", out, err)
		}
	}
	if notices, err := reopened.GrowthNotices(ctx, a.ID); err != nil || len(notices) != 0 {
		t.Fatalf("undone notice=%+v %v", notices, err)
	}
	if got, err := s.State(ctx, a.ID); err != nil || !reflect.DeepEqual(got, state) {
		t.Fatalf("state changed=%+v %v", got, err)
	}
	if got, err := s.Prepare(ctx, "other", other.ID); err != nil || got.Revision != 1 || got.Layers != before.Layers {
		t.Fatalf("other=%+v %v", got, err)
	}
	c.Expected = 1
	if _, err := s.AdoptGrowth(ctx, other.ID, "person", c); err != nil {
		t.Fatalf("suppression crossed agents: %v", err)
	}
}

func TestGrowthAtomicFailuresAndStaleUndo(t *testing.T) {
	s, db, _ := setup(t)
	a := create(t, s, "")
	ctx := t.Context()
	c := agents.GrowthCandidate{ID: "first", Expected: 1, Snapshot: "fixture-one", Notice: "fixture-notice"}
	rolledBack := errors.New("injected later write failure")
	err := db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := s.AdoptGrowthTx(ctx, tx, a.ID, "person", c); err != nil {
			return err
		}
		return rolledBack
	})
	if !errors.Is(err, rolledBack) {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, a.ID); err != nil || got.Revision != 1 {
		t.Fatalf("partial revision=%+v %v", got, err)
	}
	if notices, err := s.GrowthNotices(ctx, a.ID); err != nil || len(notices) != 0 {
		t.Fatalf("partial notice=%+v %v", notices, err)
	}
	first, err := s.AdoptGrowth(ctx, a.ID, "person", c)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []agents.GrowthCandidate{
		{ID: "stale", Expected: 1, Snapshot: "fixture-two", Notice: "fixture-notice"},
		{ID: "noop", Expected: 2, Snapshot: c.Snapshot, Notice: "fixture-notice"},
		{ID: "multiline", Expected: 2, Snapshot: "fixture-two", Notice: "first\nsecond"},
	} {
		if out, err := s.AdoptGrowth(ctx, a.ID, "person", bad); err == nil || out.Notice != "" {
			t.Fatalf("bad candidate=%+v %v", out, err)
		}
	}
	second, err := s.AdoptGrowth(ctx, a.ID, "person", agents.GrowthCandidate{ID: "second", Expected: 2, Snapshot: "fixture-two", Notice: c.Notice})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UndoGrowth(ctx, a.ID, "person", first.Undo); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("stale undo=%v", err)
	}
	if _, err := s.UndoGrowth(ctx, a.ID, "person", second.Undo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UndoGrowth(ctx, a.ID, "person", first.Undo); err != nil {
		t.Fatalf("undo chain=%v", err)
	}
	// No current persona, notice or suppression may outlive permanent deletion.
	now := time.Unix(100, 0)
	if err := s.Delete(ctx, a.ID, "person", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(ctx, now.Add(agents.TrashRetention)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrowthNotices(ctx, a.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("expired notices=%v", err)
	}
}

func TestGrowthFailedPublicationReturnsNoNotice(t *testing.T) {
	s, db, _ := setup(t)
	a := create(t, s, "")
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_growth BEFORE INSERT ON agent_growth_steps BEGIN SELECT RAISE(ABORT,'injected'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.AdoptGrowth(t.Context(), a.ID, "person", agents.GrowthCandidate{ID: "fixture", Expected: 1, Snapshot: "fixture-growth", Notice: "fixture-notice"})
	if err == nil || out.Notice != "" {
		t.Fatalf("failed publication=%+v %v", out, err)
	}
	if a, err := s.Get(context.Background(), a.ID); err != nil || a.Revision != 1 {
		t.Fatalf("revision=%+v %v", a, err)
	}
}

func TestGrowthUndoCannotSkipLaterStepWithSameSnapshot(t *testing.T) {
	s, _, _ := setup(t)
	a := create(t, s, "")
	var first agents.GrowthNotice
	for i, snapshot := range []string{"fixture-a", "fixture-b", "fixture-a"} {
		step, err := s.AdoptGrowth(t.Context(), a.ID, "person", agents.GrowthCandidate{ID: snapshot + string(rune('0'+i)), Expected: int64(i + 1), Snapshot: snapshot, Notice: "fixture-notice"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = step
		}
	}
	if _, err := s.UndoGrowth(t.Context(), a.ID, "person", first.Undo); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("skipped later growth: %v", err)
	}
}
