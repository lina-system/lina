// SPDX-License-Identifier: Apache-2.0
package binding

import (
	"fmt"
	"testing"
	"time"
)

func TestRulesAndFocus(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	base := State{Clock: now, Focus: Target{Class: "existing", ID: "a", Kind: "project"}, FocusAgeSeconds: 1, Items: []Item{{ID: "a", Title: "Garden", Kind: "project", State: "active", LastActivity: now}, {ID: "b", Title: "도서관", Names: []string{"Straße", "Café"}, Kind: "project", State: "active", LastActivity: now}}, Surfaces: map[string]string{"reply": "b"}}
	for _, tc := range []struct {
		name, text, reply, want, rule string
		age                           int64
		finished, ambiguous           bool
	}{
		{name: "reply precedence", text: "Garden", reply: "reply", want: "b", rule: "reply-or-card"},
		{name: "particle", text: "도서관에서 시작", want: "b", rule: "exact-reference"},
		{name: "case fold", text: "STRASSE", want: "b", rule: "exact-reference"},
		{name: "NFC", text: "Cafe\u0301", want: "b", rule: "exact-reference"},
		{name: "substring", text: "Gardener", want: "a"},
		{name: "two names", text: "Garden 도서관", want: "a"},
		{name: "finished", text: "도서관", want: "a", finished: true},
		{name: "ambiguous alias", text: "Straße", want: "a", ambiguous: true},
		{name: "fresh", text: "status new remember stop", want: "a", age: 3599},
		{name: "aged", text: "continue", age: 3600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			s.Items = append([]Item(nil), base.Items...)
			s.FocusAgeSeconds = tc.age
			if tc.finished {
				s.Items[1].State = "completed"
			}
			if tc.ambiguous {
				s.Items[0].Names = []string{"Straße"}
			}
			d := Resolve(s, Input{Text: tc.text, ReplyTo: tc.reply})
			if d.Target.ID != tc.want || d.Rule != tc.rule || d.Unresolved != (tc.rule == "") {
				t.Fatalf("%+v", d)
			}
		})
	}
	for _, answer := range []string{"1", "Table", "YES", "아니요"} {
		s := base
		s.Questions = []Question{{ID: "q", Target: "b", Options: []string{"Table", "List"}, YesNo: true}}
		d := Resolve(s, Input{Text: answer})
		if d.Target.ID != "b" || d.Rule != "offered-option" {
			t.Fatalf("%q: %+v", answer, d)
		}
		s.Questions = append(s.Questions, Question{ID: "q2", Target: "a", Options: []string{"Table", "List"}})
		if d := Resolve(s, Input{Text: answer}); !d.Unresolved {
			t.Fatalf("two offered questions: %+v", d)
		}
	}
}
func TestCandidateOrderAndLimit(t *testing.T) {
	now := time.Now().UTC()
	s := State{Clock: now, Focus: Target{Class: "existing", ID: "focus", Kind: "project"}, Questions: []Question{{ID: "q", Target: "question", Options: []string{"A", "B"}}}}
	for n, id := range []string{"recent", "named", "run", "question", "focus", "old1", "old2", "old3", "old4"} {
		kind := "project"
		state := "active"
		if id == "run" {
			kind = "run"
			state = "running"
		}
		s.Items = append(s.Items, Item{ID: id, Title: fmt.Sprintf("Item %s", id), Kind: kind, State: state, LastActivity: now.Add(-time.Duration(n) * time.Minute), StartedHere: id == "run"})
	}
	d := Resolve(s, Input{Text: "Item named"})
	if len(d.Candidates) != 8 {
		t.Fatalf("%+v", d)
	}
	for n, id := range []string{"focus", "question", "run", "named", "recent", "old1", "old2", "old3"} {
		if d.Candidates[n].ID != id {
			t.Fatalf("rank %d: %+v", n, d.Candidates)
		}
	}
}
