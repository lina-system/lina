// SPDX-License-Identifier: Apache-2.0
package intakereplay

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// These are measurement baselines, including their known mistakes. In
// particular neither understands new children, splits requests, nor asks.
func Decide(router string, state State, input Input) []Binding {
	ids := make([]string, 0, len(state.Items))
	for _, i := range state.Items {
		ids = append(ids, i.ID)
	}
	pick := func(target Target, decider string) []Binding {
		return []Binding{{Target: target, Decider: decider, Candidates: ids}}
	}
	if router == "rules-then-focus" {
		for _, surface := range []string{input.ReplyTo, input.Card, input.Thread} {
			if id := state.Surfaces[surface]; surface != "" && id != "" {
				return pick(itemTarget(state, id), "reply-or-card")
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
		if len(matches) == 1 && open(matches[0]) {
			return pick(itemTarget(state, matches[0].ID), "exact-reference")
		}
		var questions []Question
		for _, q := range state.Questions {
			if q.YesNo || len(q.Options) > 0 {
				questions = append(questions, q)
			}
		}
		if len(questions) == 1 {
			q := questions[0]
			answer := fold(strings.TrimSpace(input.Text))
			for n, label := range q.Options {
				if answer == strconv.Itoa(n+1) || answer == fold(label) {
					return pick(itemTarget(state, q.Target), "offered-option")
				}
			}
			if q.YesNo {
				switch answer {
				case "yes", "no", "네", "예", "아니요":
					return pick(itemTarget(state, q.Target), "offered-option")
				}
			}
		}
	}
	return pick(state.Focus, "focus")
}
func itemTarget(s State, id string) Target {
	for _, i := range s.Items {
		if i.ID == id {
			return Target{Class: "existing", ID: id, Kind: i.Kind}
		}
	}
	return Target{Class: "none"}
}
func open(i Item) bool     { return i.State != "completed" && i.State != "canceled" && i.State != "closed" }
func fold(s string) string { return cases.Fold().String(norm.NFC.String(s)) }
func word(r rune) bool     { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' }
func exactMention(text, name string) bool {
	text, name = fold(text), fold(name)
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
