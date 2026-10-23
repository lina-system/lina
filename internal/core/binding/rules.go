// SPDX-License-Identifier: Apache-2.0
package binding

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule checks the complete authorized snapshot so truncating the model's
// candidate list cannot turn two matching names into an apparently unique one.
func Rule(state State, input Input) (Target, string) {
	matched := func(t Target, rule string) (Target, string) {
		if t.Class != "existing" {
			return Target{}, ""
		}
		return t, rule
	}
	for _, surface := range []string{input.ReplyTo, input.Card, input.Thread} {
		if id := state.Surfaces[surface]; surface != "" && id != "" {
			return matched(itemTarget(state, id), "reply-or-card")
		}
	}
	var matches []Item
	for _, i := range state.Items {
		names := append([]string{i.ID, i.Title}, i.Names...)
		names = append(names, i.Links...)
		for _, name := range names {
			if exactMention(input.Text, name) {
				matches = append(matches, i)
				break
			}
		}
	}
	if len(matches) == 1 && Open(matches[0]) {
		return matched(itemTarget(state, matches[0].ID), "exact-reference")
	}
	var questions []Question
	for _, q := range state.Questions {
		if q.YesNo || len(q.Options) > 0 {
			questions = append(questions, q)
		}
	}
	if len(questions) == 1 {
		q := questions[0]
		answer := Fold(strings.TrimSpace(input.Text))
		for n, label := range q.Options {
			if answer == strconv.Itoa(n+1) || answer == Fold(label) {
				return matched(itemTarget(state, q.Target), "offered-option")
			}
		}
		if q.YesNo {
			switch answer {
			case "yes", "no", "네", "예", "아니요":
				return matched(itemTarget(state, q.Target), "offered-option")
			}
		}
	}
	return Target{}, ""
}

func Resolve(state State, input Input) Decision {
	candidates := Rank(state, input)
	target, rule := Rule(state, input)
	fresh := state.Focus.Class == "existing" && state.FocusAgeSeconds < 3600
	if rule == "" {
		target = Target{Class: "none"}
		if fresh {
			target = state.Focus
		}
	}
	decider := rule
	if decider == "" {
		decider = "no-rule"
	}
	return Decision{Target: target, Candidates: candidates, Rule: rule, Decider: decider, Unresolved: rule == "", FocusFresh: fresh}
}

func Rank(state State, input Input) []Item {
	questions := map[string]bool{}
	for _, q := range state.Questions {
		questions[q.Target] = true
	}
	rank := func(i Item) int {
		if state.Focus.Class == "existing" && i.ID == state.Focus.ID {
			return 0
		}
		if questions[i.ID] {
			return 1
		}
		if i.StartedHere && (i.Kind == "task" || i.Kind == "run") && (i.State == "running" || i.State == "working" || i.State == "waiting" || i.State == "needs_input") {
			return 2
		}
		if Named(input.Text, i) {
			return 3
		}
		return 4
	}
	items := make([]Item, 0, len(state.Items))
	seen := map[string]bool{}
	for _, i := range state.Items {
		if seen[i.ID] {
			continue
		}
		seen[i.ID] = true
		if rank(i) == 4 && (i.Kind == "task" || i.Kind == "run") {
			continue
		}
		items = append(items, i)
	}
	sort.SliceStable(items, func(a, b int) bool {
		x, y := items[a], items[b]
		if rank(x) != rank(y) {
			return rank(x) < rank(y)
		}
		if !x.LastActivity.Equal(y.LastActivity) {
			return x.LastActivity.After(y.LastActivity)
		}
		return x.ID < y.ID
	})
	items = items[:min(8, len(items))]
	for n := range items {
		items[n].Names = nil
		items[n].Links = nil
		items[n].StartedHere = false
	}
	return items
}
func Named(text string, i Item) bool {
	for _, name := range append(append([]string{i.ID, i.Title}, i.Names...), i.Links...) {
		if exactMention(text, name) {
			return true
		}
	}
	return false
}
func itemTarget(s State, id string) Target {
	for _, i := range s.Items {
		if i.ID == id {
			return Target{Class: "existing", ID: id, Kind: i.Kind}
		}
	}
	return Target{Class: "none"}
}
func Open(i Item) bool {
	switch i.State {
	case "completed", "canceled", "closed", "done", "stopped", "landed", "cancelled":
		return false
	}
	return true
}
func Fold(s string) string { return cases.Fold().String(norm.NFC.String(s)) }
func word(r rune) bool     { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' }
func exactMention(text, name string) bool {
	text, name = Fold(text), Fold(name)
	if name == "" {
		return false
	}
	for offset := 0; offset < len(text); {
		n := strings.Index(text[offset:], name)
		if n < 0 {
			return false
		}
		n += offset
		end := n + len(name)
		left := n == 0
		if n > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:n])
			left = !word(r)
		}
		right := end == len(text)
		if !right {
			r, _ := utf8.DecodeRuneInString(text[end:])
			right = !word(r)
		}
		if left && !right {
			for _, particle := range []string{"으로", "에서", "에게", "에는", "은", "는", "이", "가", "을", "를", "에", "로", "와", "과", "도", "만", "의"} {
				suffix := text[end:]
				if strings.HasPrefix(suffix, particle) {
					tail := suffix[len(particle):]
					r, _ := utf8.DecodeRuneInString(tail)
					if tail == "" || !word(r) {
						right = true
						break
					}
				}
			}
		}
		if left && right {
			return true
		}
		offset = end
	}
	return false
}
