// SPDX-License-Identifier: Apache-2.0
package binding

import "testing"

func TestAskRequiresTieAndIrreversibleAction(t *testing.T) {
	s := State{Items: []Item{{ID: "a", Kind: "task", State: "running"}, {ID: "b", Kind: "task", State: "running"}}}
	a, b := Target{Class: "existing", Kind: "task", ID: "a"}, Target{Class: "existing", Kind: "task", ID: "b"}
	for _, action := range []string{"read", "edit", "steer", "start", "send", "pay", "delete"} {
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
func TestDuplicateNormalizationAndClosedItems(t *testing.T) {
	s := State{Items: []Item{{ID: "nfc", Title: "Café", State: "open"}, {ID: "closed", Title: "Cafe\u0301", State: "done"}, {ID: "near", Title: "Another title", State: "open"}}}
	matches := Duplicates(s, " CAFE\u0301 ", []string{"near"})
	if len(matches) != 2 || matches[0].ID != "nfc" || matches[1].ID != "near" {
		t.Fatal(matches)
	}
}
