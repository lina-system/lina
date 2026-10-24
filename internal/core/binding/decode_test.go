// SPDX-License-Identifier: Apache-2.0
package binding

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func decodeState() State {
	now := time.Now().UTC()
	return State{Clock: now, Focus: Target{Class: "existing", ID: "garden", Kind: "project"}, Items: []Item{
		{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now, Names: []string{"정원"}},
		{ID: "library", Title: "Library", Kind: "project", State: "active", LastActivity: now},
		{ID: "finished", Title: "Done", Kind: "project", State: "completed", LastActivity: now},
		{ID: "planting", Title: "Planting", Kind: "task", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
}

// A model writes the shapes it reads: ids, titles, class words and ordinals are
// all accepted, and only a target it cannot resolve is returned for correction.
func TestNormalizeChoicesAcceptsModelShapes(t *testing.T) {
	s := decodeState()
	text := "Water the garden and start a new task."
	for _, tc := range []struct {
		name string
		raw  string
		want Choice
	}{
		{"id", `{"request":0,"quote":"Water the garden","target":"garden"}`, Choice{Request: 0, Quote: "Water the garden", Target: Target{Class: "existing", ID: "garden", Kind: "project"}}},
		{"title", `{"target":{"id":"Library"}}`, Choice{Request: -1, Target: Target{Class: "existing", ID: "library", Kind: "project"}}},
		{"name", `{"target":"정원"}`, Choice{Request: -1, Target: Target{Class: "existing", ID: "garden", Kind: "project"}}},
		{"object", `{"target":{"class":"existing","id":"planting","kind":"task"},"action":"follow"}`, Choice{Request: -1, Target: Target{Class: "existing", ID: "planting", Kind: "task"}, Action: "steer"}},
		{"stop", `{"target":"planting","action":"stop"}`, Choice{Request: -1, Target: Target{Class: "existing", ID: "planting", Kind: "task"}, Action: "stop"}},
		{"new-kind-word", `{"target":"new todo"}`, Choice{Request: -1, Target: Target{Class: "new", Kind: "todo"}}},
		{"parent-dropped", `{"target":{"class":"new","kind":"todo","parent":"planting"}}`, Choice{Request: -1, Target: Target{Class: "new", Kind: "todo"}}},
		{"new-with-match", `{"target":{"class":"new","kind":"todo","id":"garden"}}`, Choice{Request: -1, Target: Target{Class: "existing", ID: "garden", Kind: "project"}}},
		{"none", `{"target":"none","quote":"this text is not in the input"}`, Choice{Request: -1, Quote: text, Target: Target{Class: "none"}}},
		{"string-request", `{"request":"2","target":"none"}`, Choice{Request: 2, Quote: text, Target: Target{Class: "none"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeChoices(s, text, []json.RawMessage{json.RawMessage(tc.raw)})
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			want.Quote = firstNonEmpty(want.Quote, text)
			if len(got) != 1 || got[0].Request != want.Request || got[0].Target != want.Target || got[0].Action != want.Action || got[0].Quote != want.Quote {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}
}

// An unknown or ambiguous target is the one argument Core must not guess: it
// returns the authorized ids so the next call can resolve it.
func TestNormalizeChoicesCorrectsOnlyTargets(t *testing.T) {
	s := decodeState()
	s.Items = append(s.Items, Item{ID: "beds-a", Title: "Beds", Kind: "project", State: "active", LastActivity: s.Clock}, Item{ID: "beds-b", Title: "Beds", Kind: "project", State: "active", LastActivity: s.Clock})
	for _, tc := range []struct {
		name, raw, field, reason string
		allowed                  []string
	}{
		{"unknown", `{"target":"kitchen"}`, "bindings[0].target", "unknown_target", []string{"garden", "library", "planting", "beds-a", "beds-b"}},
		{"ambiguous", `{"target":"Beds"}`, "bindings[0].target", "ambiguous_target", []string{"beds-a", "beds-b"}},
		{"missing", `{"quote":"Water the garden"}`, "bindings[0].target", "missing_target", []string{"existing", "new", "none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeChoices(s, "Water the garden", []json.RawMessage{json.RawMessage(tc.raw)})
			var detail *ChoiceError
			if !errors.As(err, &detail) || detail.Field != tc.field || detail.Reason != tc.reason || !slices.Equal(detail.Allowed, tc.allowed) {
				t.Fatalf("error %+v", err)
			}
		})
	}
}

// A unique exact title of a finished offered item is that item. An id already
// binds it; the title must not become an unknown target after the open filter.
func TestCompletedExactTitleBinds(t *testing.T) {
	s := decodeState()
	got, err := NormalizeChoices(s, "Water the garden", []json.RawMessage{json.RawMessage(`{"target":"Done"}`)})
	if err != nil || len(got) != 1 || got[0].Target != (Target{Class: "existing", ID: "finished", Kind: "project"}) {
		t.Fatalf("completed title: %+v %v", got, err)
	}
	s.Items = append(s.Items, Item{ID: "also-done", Title: "Done", Kind: "project", State: "completed", LastActivity: s.Clock})
	_, err = NormalizeChoices(s, "Water the garden", []json.RawMessage{json.RawMessage(`{"target":"Done"}`)})
	var detail *ChoiceError
	if !errors.As(err, &detail) || detail.Reason != "ambiguous_target" || !slices.Equal(detail.Allowed, []string{"finished", "also-done"}) {
		t.Fatalf("two finished titles: %+v", err)
	}
	s.Items = append(s.Items, Item{ID: "open-done", Title: "Done", Kind: "project", State: "active", LastActivity: s.Clock})
	got, err = NormalizeChoices(s, "Water the garden", []json.RawMessage{json.RawMessage(`{"target":"Done"}`)})
	if err != nil || len(got) != 1 || got[0].Target.ID != "open-done" {
		t.Fatalf("open title: %+v %v", got, err)
	}
}

// An ambiguous tied alternative is a correction, not a settled recommendation.
func TestAmbiguousTiedTargetIsNotDropped(t *testing.T) {
	s := decodeState()
	s.Items = append(s.Items, Item{ID: "beds-a", Title: "Beds", Kind: "project", State: "active", LastActivity: s.Clock}, Item{ID: "beds-b", Title: "Beds", Kind: "project", State: "active", LastActivity: s.Clock})
	_, err := NormalizeChoices(s, "Stop the garden work.", []json.RawMessage{json.RawMessage(`{"target":"garden","tied":["garden","Beds"],"action":"stop"}`)})
	var detail *ChoiceError
	if !errors.As(err, &detail) || detail.Field != "bindings[0].target" || detail.Reason != "ambiguous_target" || !slices.Equal(detail.Allowed, []string{"beds-a", "beds-b"}) {
		t.Fatalf("error %+v", err)
	}
	got, err := NormalizeChoices(s, "Stop the garden work.", []json.RawMessage{json.RawMessage(`{"target":"garden","tied":["garden","library"],"action":"stop"}`)})
	if err != nil || len(got) != 1 || len(got[0].Tied) != 2 || got[0].Action != "stop" {
		t.Fatalf("resolvable tie: %+v %v", got, err)
	}
}

// Close matches that are not open items never reach the duplicate check, and a
// difference is bounded so one message cannot store unbounded text.
func TestNormalizeChoicesBoundsMetadata(t *testing.T) {
	s := decodeState()
	raw, _ := json.Marshal(map[string]any{"target": "none", "close_matches": []string{"garden", "finished", "missing"}, "difference": strings.Repeat("x", maxDifference+10)})
	got, err := NormalizeChoices(s, "Save a task.", []json.RawMessage{raw})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[0].Close, []string{"garden"}) || len(got[0].Difference) > maxDifference {
		t.Fatalf("metadata: %+v", got[0])
	}
}

// A candidate id that begins with a stored kind name is still that item.
// Kind-and-colon text is new work only when it is not an exact candidate id.
func TestExactCandidateIDBeatsKindPrefix(t *testing.T) {
	s := decodeState()
	s.Items = append(s.Items, Item{ID: "task:a", Title: "Recovered", Kind: "task", State: "running", LastActivity: s.Clock})
	text := "Continue the recovered task."
	got, err := NormalizeChoices(s, text, []json.RawMessage{json.RawMessage(`{"target":"task:a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := Target{Class: "existing", ID: "task:a", Kind: "task"}
	if len(got) != 1 || got[0].Target != want {
		t.Fatalf("exact id: got %+v want %+v", got, want)
	}
	folded, err := NormalizeChoices(s, text, []json.RawMessage{json.RawMessage(`{"target":"Task:A"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(folded) != 1 || folded[0].Target != want {
		t.Fatalf("folded id: got %+v want %+v", folded, want)
	}
	fresh, err := NormalizeChoices(s, text, []json.RawMessage{json.RawMessage(`{"target":"task:brand-new"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].Target != (Target{Class: "new", Kind: "task"}) {
		t.Fatalf("kind shorthand: got %+v", fresh)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
