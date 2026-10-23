// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestDispositionChangesOnlyThroughMeasuredGrowthAndUndo(t *testing.T) {
	s, _, _ := setup(t)
	a := create(t, s, "")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := agents.DefaultEmotionPolicy()
	before, err := s.Prepare(t.Context(), "before-growth", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.Affect = &agents.EmotionRecord{Disposition: agents.Affect{Valence: 1}}
	if err := s.SetState(t.Context(), a.ID, state, "person"); !errors.Is(err, agents.ErrInvalid) {
		t.Fatalf("raw disposition mutation accepted: %v", err)
	}
	candidate := agents.DispositionCandidate{Snapshot: "fixture growth snapshot", Disposition: agents.Affect{Valence: 0.4}}
	if _, err := s.AdoptDispositionGrowth(t.Context(), a.ID, a.Revision, agents.GrowthProof{}, "person", now, policy); err == nil {
		t.Fatal("unmeasured growth adopted")
	}
	calls := map[string]int{}
	evaluation := agents.GrowthEvaluation{Metric: "fixture squared error", SetID: "held-out-fixture", Cases: []agents.GrowthCase{{ID: "one"}, {ID: "two"}}, Score: func(c agents.DispositionCandidate, example agents.GrowthCase) (float64, error) {
		calls[example.ID]++
		d := c.Disposition.Valence - 0.4
		return -d * d, nil
	}}
	proof, err := s.MeasureDispositionGrowth(t.Context(), a.ID, a.Revision, candidate, evaluation)
	if err != nil {
		t.Fatal(err)
	}
	if calls["one"] != 2 || calls["two"] != 2 {
		t.Fatal("baseline and candidate did not receive the same evaluation set")
	}
	grown, err := s.AdoptDispositionGrowth(t.Context(), a.ID, a.Revision, proof, "person", now, policy)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := agents.ProjectEmotion(state, "chat", before.Revision, now, policy, false)
	if err != nil || old.Disposition != (agents.Affect{}) {
		t.Fatalf("growth applied to old preparation: %+v %v", old, err)
	}
	next, err := s.Prepare(t.Context(), "after-growth", a.ID)
	if err != nil || next.Revision != grown.Revision || next.Layers.GrowthSnapshot != candidate.Snapshot {
		t.Fatalf("growth not adopted: %+v %v", next, err)
	}
	view, err := agents.ProjectEmotion(state, "chat", next.Revision, now, policy, false)
	if err != nil || view.Disposition != candidate.Disposition {
		t.Fatalf("growth baseline missing: %+v %v", view, err)
	}
	undone, err := s.UndoDispositionGrowth(t.Context(), a.ID, grown.Revision, "person", now, policy)
	if err != nil || undone.Revision != grown.Revision+1 {
		t.Fatalf("undo: %+v %v", undone, err)
	}
	restored, err := s.Prepare(t.Context(), "after-growth-undo", a.ID)
	if err != nil || restored.Layers != before.Layers {
		t.Fatalf("growth text not restored: %+v %v", restored, err)
	}
	state, err = s.State(t.Context(), a.ID)
	if err != nil || state.Affect.Disposition != (agents.Affect{}) {
		t.Fatalf("baseline not restored: %+v %v", state, err)
	}
	if _, err := s.MeasureDispositionGrowth(t.Context(), a.ID, undone.Revision, candidate, evaluation); err == nil {
		t.Fatal("undone growth proposed again")
	}
}

func TestGrowthWithoutImprovementStaysOff(t *testing.T) {
	s, _, _ := setup(t)
	a := create(t, s, "")
	proof, err := s.MeasureDispositionGrowth(t.Context(), a.ID, a.Revision, agents.DispositionCandidate{Snapshot: "fixture", Disposition: agents.Affect{Valence: 0.1}}, agents.GrowthEvaluation{Metric: "constant fixture", SetID: "fixture", Cases: []agents.GrowthCase{{ID: "one"}}, Score: func(agents.DispositionCandidate, agents.GrowthCase) (float64, error) { return 1, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptDispositionGrowth(t.Context(), a.ID, a.Revision, proof, "person", time.Now(), agents.DefaultEmotionPolicy()); err == nil {
		t.Fatal("non-improving mechanism adopted")
	}
	current, err := s.Get(t.Context(), a.ID)
	if err != nil || current.Revision != a.Revision {
		t.Fatal("failed growth revised persona")
	}
}
