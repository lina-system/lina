// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestFillBlankIsAgentSpecificAndSurvivesPresetUpdates(t *testing.T) {
	s, db, root := setup(t)
	preset := agents.Preset{ID: "fixture", Core: "fixture core", BlankSettings: []string{"likes", "birthday"}}
	if err := s.UpdatePreset(t.Context(), preset, "person"); err != nil {
		t.Fatal(err)
	}
	a, b := create(t, s, preset.ID), create(t, s, preset.ID)
	before, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	filled, err := s.FillBlank(t.Context(), a.ID, agents.StateRevision(before), "likes", "violet", "event:confirmed", "person")
	if err != nil || !filled {
		t.Fatalf("fill failed: %v %v", filled, err)
	}
	state, err := s.State(t.Context(), a.ID)
	if err != nil || state.Settings["likes"] != "violet" || state.SettingSources["likes"] != "event:confirmed" {
		t.Fatalf("missing value/evidence: %+v %v", state, err)
	}
	if again, err := s.FillBlank(t.Context(), a.ID, agents.StateRevision(state), "likes", "violet", "event:repeat", "person"); err != nil || again {
		t.Fatalf("duplicate fill changed source: %v %v", again, err)
	}
	if _, err := s.FillBlank(t.Context(), a.ID, agents.StateRevision(state), "likes", "green", "event:conflict", "person"); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("filled value overwritten: %v", err)
	}
	if _, err := s.FillBlank(t.Context(), a.ID, agents.StateRevision(state), "unknown", "value", "event:unknown", "person"); !errors.Is(err, agents.ErrInvalid) {
		t.Fatalf("undefined blank created: %v", err)
	}
	if _, err := s.FillBlank(t.Context(), a.ID, agents.StateRevision(before), "birthday", "January 1", "event:stale", "person"); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("stale state write accepted: %v", err)
	}
	preset.Core = "updated fixture core"
	preset.BlankSettings = []string{"new-blank"}
	if err := s.UpdatePreset(t.Context(), preset, "person"); err != nil {
		t.Fatal(err)
	}
	after, err := s.State(t.Context(), a.ID)
	if err != nil || !reflect.DeepEqual(after, state) {
		t.Fatalf("preset changed settings: %+v %v", after, err)
	}
	other, err := s.State(t.Context(), b.ID)
	if err != nil || other.Settings["likes"] != "" || len(other.SettingSources) != 0 {
		t.Fatalf("other agent changed: %+v %v", other, err)
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.State(t.Context(), a.ID)
	if err != nil || !reflect.DeepEqual(persisted, state) {
		t.Fatalf("settings lost after reopen: %+v %v", persisted, err)
	}
}

func TestFillBlankNeedsAValueAndEvidence(t *testing.T) {
	s, _, _ := setup(t)
	if err := s.UpdatePreset(t.Context(), agents.Preset{ID: "fixture", BlankSettings: []string{"likes"}}, "person"); err != nil {
		t.Fatal(err)
	}
	a := create(t, s, "fixture")
	state, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ value, source, person string }{{"", "event", "person"}, {"value", "", "person"}, {"value", "event", ""}, {"password=synthetic-secret-value", "event", "person"}} {
		if filled, err := s.FillBlank(t.Context(), a.ID, agents.StateRevision(state), "likes", tc.value, tc.source, tc.person); err == nil || filled {
			t.Fatalf("invalid fill accepted: %v %v", filled, err)
		}
	}
}

func TestConcurrentBlankFillsRejectStaleState(t *testing.T) {
	s, _, _ := setup(t)
	if err := s.UpdatePreset(t.Context(), agents.Preset{ID: "fixture", BlankSettings: []string{"likes", "birthday"}}, "person"); err != nil {
		t.Fatal(err)
	}
	a := create(t, s, "fixture")
	state, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := agents.StateRevision(state)
	results := make(chan error, 2)
	for _, name := range []string{"likes", "birthday"} {
		go func() {
			_, err := s.FillBlank(t.Context(), a.ID, expected, name, "fixture value", "event:"+name, "person")
			results <- err
		}()
	}
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, agents.ErrConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent fills: %d applied, %d conflicts", succeeded, conflicted)
	}
	state, err = s.State(t.Context(), a.ID)
	if err != nil || len(state.SettingSources) != 1 {
		t.Fatalf("lost or duplicated state: %+v %v", state, err)
	}
}
