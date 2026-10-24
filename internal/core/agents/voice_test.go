// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestVoiceChangeAndUndoApplyOnFollowingTurn(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	a, other := create(t, s, ""), create(t, s, "")
	before, err := s.Prepare(t.Context(), "asking-turn", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.ChangeVoice(t.Context(), a.ID, a.Revision, "fixture casual voice", "person")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision != before.Revision+1 {
		t.Fatal("voice change did not create a revision")
	}
	same, err := s.Prepare(t.Context(), "asking-turn", a.ID)
	if err != nil || same.Layers != before.Layers {
		t.Fatalf("voice changed within a turn: %+v %v", same, err)
	}
	next, err := s.Prepare(t.Context(), "following-turn", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := before.Layers
	want.UserVoice = "fixture casual voice"
	if next.Layers != want {
		t.Fatalf("voice change touched other layers: %+v", next.Layers)
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	undone, err := reopened.UndoVoice(t.Context(), a.ID, changed.Revision, "person")
	if err != nil {
		t.Fatal(err)
	}
	if undone.Revision != changed.Revision+1 {
		t.Fatal("undo rewrote history")
	}
	restored, err := s.Prepare(t.Context(), "after-undo", a.ID)
	if err != nil || restored.Layers != before.Layers {
		t.Fatalf("previous voice not restored: %+v %v", restored, err)
	}
	if _, err := s.UndoVoice(t.Context(), a.ID, changed.Revision, "person"); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("stale undo accepted: %v", err)
	}
	unchanged, err := s.Prepare(t.Context(), "other-agent", other.ID)
	if err != nil || unchanged.Layers != before.Layers {
		t.Fatalf("other agent changed: %+v %v", unchanged, err)
	}
}

func TestVoiceUndoCannotUndoAnotherKindOfRevision(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := create(t, s, "")
	p, err := s.Prepare(t.Context(), "baseline", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Layers.GrowthSnapshot = "fixture growth"
	grown, err := s.Revise(t.Context(), a.ID, a.Revision, p.Layers, "person")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UndoVoice(t.Context(), a.ID, grown.Revision, "person"); !errors.Is(err, agents.ErrInvalid) {
		t.Fatalf("voice undo accepted growth: %v", err)
	}
}
