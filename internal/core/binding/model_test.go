// SPDX-License-Identifier: Apache-2.0
package binding

import "testing"

func TestAskRequiresTieAndIrreversibleAction(t *testing.T) {
	s := State{Items: []Item{{ID: "a", Kind: "task", State: "running"}, {ID: "b", Kind: "task", State: "running"}}}
	a, b := Target{Class: "existing", Kind: "task", ID: "a"}, Target{Class: "existing", Kind: "task", ID: "b"}
	for _, action := range []string{"read", "edit", "steer", "start", "send", "pay", "delete", "stop"} {
		c := Choice{Quote: "do that", Target: a, Tied: []Target{a, b}, Action: action}
		if err := ValidateChoice(s, "do that", c); err != nil {
			t.Fatal(err)
		}
		if NeedsQuestion(s, c) != (action != "read" && action != "edit") {
			t.Fatalf("action %s", action)
		}
		c.Tied = nil
		if NeedsQuestion(s, c) {
			t.Fatalf("single target asked: %s", action)
		}
	}
}
func TestQuestionPageKeepsEveryInheritedTarget(t *testing.T) {
	targets := make([]Target, 10)
	items := make([]Item, len(targets))
	for i := range targets {
		id := string(rune('a' + i))
		targets[i] = Target{Class: "existing", Kind: "task", ID: id}
		items[i] = Item{ID: id, Kind: "task", State: "waiting"}
	}
	s := State{Items: items}
	shown, rest := QuestionPage(targets[:6])
	if len(shown) != 4 || len(rest) != 2 {
		t.Fatalf("page of six: %d shown, %d rest", len(shown), len(rest))
	}
	shown[0].ID = "changed"
	if targets[0].ID == "changed" {
		t.Fatal("page shared storage with the inherited targets")
	}
	shown, rest = QuestionPage(targets[:6])
	choice := Choice{Quote: "stop it", Target: shown[0], Tied: shown, More: rest, Action: "stop"}
	if err := ValidateChoice(s, "stop it", choice); err != nil {
		t.Fatal(err)
	}
	choice.More = append([]Target{shown[0]}, rest...)
	if err := ValidateChoice(s, "stop it", choice); err == nil {
		t.Fatal("a repeated target was accepted on the next page")
	}
	choice = Choice{Quote: "stop it", Target: targets[0], Tied: targets[:5], More: targets[5:6], Action: "stop"}
	if err := ValidateChoice(s, "stop it", choice); err == nil {
		t.Fatal("five shown targets plus a later page was accepted")
	}
	choice.More = nil
	choice.Tied = targets[:6]
	if err := ValidateChoice(s, "stop it", choice); err == nil {
		t.Fatal("six tied targets were accepted")
	}
	shown, rest = QuestionPage(targets)
	shown, rest = QuestionPage(rest)
	if len(shown) != 4 || len(rest) != 2 {
		t.Fatalf("second page: %d shown, %d rest", len(shown), len(rest))
	}
	shown, rest = QuestionPage(rest)
	if len(shown) != 2 || rest != nil {
		t.Fatalf("last page: %d shown, rest %#v", len(shown), rest)
	}
	shown, rest = QuestionPage(targets[:5])
	if len(shown) != 5 || rest != nil {
		t.Fatalf("five targets: %d shown, rest %#v", len(shown), rest)
	}
}

func TestNeedsQuestionCountsTargetsHeldForLaterPage(t *testing.T) {
	shown := make([]Target, 4)
	items := make([]Item, 0, 6)
	for i := range shown {
		id := string(rune('a' + i))
		shown[i] = Target{Class: "existing", Kind: "project", ID: id}
		items = append(items, Item{ID: id, Kind: "project", State: "active"})
	}
	later := Target{Class: "existing", Kind: "initiative", ID: "lane"}
	items = append(items, Item{ID: later.ID, Kind: "initiative", State: "active"})
	quiet := Choice{Quote: "steer it", Target: shown[0], Tied: shown, More: []Target{later}, Action: "steer"}
	if NeedsQuestion(State{Items: items}, quiet) {
		t.Fatal("a later initiative made a steer ask")
	}
	for _, state := range []string{"running", "working", "waiting", "needs_input", "input_needed"} {
		risky := Item{ID: "live", Kind: "task", State: state}
		choice := quiet
		choice.More = []Target{later, {Class: "existing", Kind: "task", ID: risky.ID}}
		held := append(append([]Item{}, items...), risky)
		if !NeedsQuestion(State{Items: held}, choice) {
			t.Fatalf("state %s on a later page did not ask", state)
		}
	}
}
func TestSteerOnAnInputNeededTaskAsks(t *testing.T) {
	a := Target{Class: "existing", Kind: "task", ID: "a"}
	b := Target{Class: "existing", Kind: "task", ID: "b"}
	// A blocked task card keeps the work state spelling, not the binding one.
	s := State{Items: []Item{{ID: "a", Kind: "task", State: "input_needed"}, {ID: "b", Kind: "task", State: "done"}}}
	if !NeedsQuestion(s, Choice{Quote: "steer it", Target: a, Tied: []Target{a, b}, Action: "steer"}) {
		t.Fatal("an input-needed task did not keep the steer pending")
	}
}
func TestDuplicateNormalizationAndClosedItems(t *testing.T) {
	s := State{Items: []Item{{ID: "nfc", Title: "Café", State: "open"}, {ID: "closed", Title: "Cafe\u0301", State: "done"}, {ID: "near", Title: "Another title", State: "open"}}}
	matches := Duplicates(s, " CAFE\u0301 ", []string{"near"})
	if len(matches) != 2 || matches[0].ID != "nfc" || matches[1].ID != "near" {
		t.Fatal(matches)
	}
}
