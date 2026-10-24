// SPDX-License-Identifier: Apache-2.0
package binding

import (
	"testing"
	"time"
)

func chipState(age int64) State {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return State{Clock: now, Focus: Target{Class: "existing", ID: "garden", Kind: "project"}, FocusAgeSeconds: age,
		Items:    []Item{{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now}, {ID: "plants", Title: "Plants", Kind: "todo", State: "open", LastActivity: now}, {ID: "walls", Title: "Walls", Kind: "todo", State: "open", LastActivity: now}, {ID: "done", Title: "Done", Kind: "todo", State: "completed", LastActivity: now}},
		Surfaces: map[string]string{}}
}

func TestChipShowsOnlyWhatLeavesTheFreshFocus(t *testing.T) {
	fresh := chipState(10)
	for _, tc := range []struct {
		name   string
		state  State
		target Target
		show   bool
	}{
		{name: "fresh focus", state: fresh, target: Target{Class: "existing", ID: "garden", Kind: "project"}},
		{name: "leaves the focus", state: fresh, target: Target{Class: "existing", ID: "plants", Kind: "todo"}, show: true},
		{name: "aged focus", state: chipState(FreshFocusSeconds), target: Target{Class: "existing", ID: "garden", Kind: "project"}, show: true},
		{name: "creates an item", state: fresh, target: Target{Class: "new", Kind: "todo"}, show: true},
		{name: "none leaves the focus", state: fresh, target: Target{Class: "none"}, show: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ChipFor(tc.state, tc.target)
			if got.Show != tc.show {
				t.Fatalf("show = %v, want %v", got.Show, tc.show)
			}
			if got.Target != tc.target {
				t.Errorf("target = %+v", got.Target)
			}
		})
	}
	empty := State{Focus: Target{Class: "none"}, Items: []Item{}, Surfaces: map[string]string{}}
	if ChipFor(empty, Target{Class: "none"}).Show {
		t.Error("an empty binding with no focus shows a chip")
	}
}

func TestOffersListTheOtherCandidates(t *testing.T) {
	state := chipState(10)
	decision := Decision{Candidates: []Item{state.Items[0], state.Items[1], state.Items[3]}}
	offers := Offers(decision, Target{Class: "existing", ID: "plants", Kind: "todo"})
	if len(offers) != 3 {
		t.Fatalf("offers = %+v", offers)
	}
	if offers[0].Target.ID != "garden" || offers[1].Target.Class != "new" || offers[2].Target.Class != "none" {
		t.Errorf("order = %+v", offers)
	}
	if strings := offers[0].Name; strings != "Garden" {
		t.Errorf("name = %q", strings)
	}
	if !Offered(offers, Target{Class: "existing", ID: "garden", Kind: "project"}) {
		t.Error("a listed candidate is not offered")
	}
	if Offered(offers, Target{Class: "existing", ID: "walls", Kind: "todo"}) {
		t.Error("an unlisted candidate is offered")
	}
	if !Offered(offers, Target{Class: "none"}) || !Offered(offers, Target{Class: "new", Kind: "todo"}) {
		t.Error("new and none must be offered")
	}
	if Offered(offers, Target{Class: "new", Kind: "project"}) {
		t.Error("a new project is offered without the model naming its kind")
	}
}

func TestTallyCountsMovesPerHundredByDecider(t *testing.T) {
	stats := Tally(map[string]int{"exact-reference": 4, "model": 0}, map[string]int{"exact-reference": 1, "model": 2})
	if stats.Bindings != 4 || stats.Moves != 3 || stats.PerHundred != 75 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(stats.Deciders) != 2 || stats.Deciders[0].Decider != "exact-reference" || stats.Deciders[0].PerHundred != 25 {
		t.Fatalf("deciders = %+v", stats.Deciders)
	}
	if stats.Deciders[1].PerHundred != 0 {
		t.Errorf("a decider with no binding must not divide: %+v", stats.Deciders[1])
	}
}
