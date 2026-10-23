// SPDX-License-Identifier: Apache-2.0
package binding

import (
	"errors"
	"slices"
	"strings"
)

var ErrChoice = errors.New("invalid_input_binding")

// Choice names one request, in message order. Tied is ordered with the
// recommended target first. Confidence is deliberately not an ask signal.
type Choice struct {
	Request       int                `json:"request"`
	Quote         string             `json:"quote"`
	Target        Target             `json:"target"`
	Tied          []Target           `json:"tied,omitempty"`
	Action        string             `json:"action,omitempty"`
	Close         []string           `json:"close_matches,omitempty"`
	Difference    string             `json:"difference,omitempty"`
	Decider       string             `json:"decider,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Question      string             `json:"question,omitempty"`
	Cancelled     bool               `json:"cancelled,omitempty"`
	CreatedID     string             `json:"created_id,omitempty"`
}

func ValidTarget(s State, target Target) bool {
	switch target.Class {
	case "none":
		return target == (Target{Class: "none"})
	case "new":
		return target.ID == "" && slices.Contains([]string{"initiative", "project", "todo", "task", "run"}, target.Kind) && (target.Parent == "" || slices.ContainsFunc(s.Items, func(i Item) bool {
			return i.ID == target.Parent && Open(i) && (i.Kind == "initiative" || i.Kind == "project")
		}))
	case "existing":
		return target.Parent == "" && slices.ContainsFunc(s.Items, func(i Item) bool { return i.ID == target.ID && i.Kind == target.Kind })
	}
	return false
}
func ValidateChoice(s State, text string, c Choice) error {
	if c.Request < 0 || c.Request >= 32 || strings.TrimSpace(c.Quote) == "" || !strings.Contains(text, c.Quote) || len(c.Difference) > 4096 || !ValidTarget(s, c.Target) {
		return ErrChoice
	}
	if c.Action != "" && !slices.Contains([]string{"read", "edit", "steer", "start", "send", "pay", "delete"}, c.Action) {
		return ErrChoice
	}
	if len(c.Tied) == 1 || len(c.Tied) > 5 {
		return ErrChoice
	}
	seen := map[Target]bool{}
	for _, target := range c.Tied {
		if seen[target] || !ValidTarget(s, target) {
			return ErrChoice
		}
		seen[target] = true
	}
	if len(c.Tied) > 0 && c.Target != c.Tied[0] {
		return ErrChoice
	}
	for _, id := range c.Close {
		if !slices.ContainsFunc(s.Items, func(i Item) bool { return i.ID == id && Open(i) }) {
			return ErrChoice
		}
	}
	return nil
}
func NeedsQuestion(s State, c Choice) bool {
	if len(c.Tied) < 2 {
		return false
	}
	switch c.Action {
	case "start", "send", "pay", "delete":
		return true
	case "steer":
		for _, target := range c.Tied {
			for _, i := range s.Items {
				if i.ID == target.ID && (i.Kind == "task" || i.Kind == "run") && slices.Contains([]string{"running", "working", "waiting", "needs_input"}, i.State) {
					return true
				}
			}
		}
	}
	return false
}
func Duplicates(s State, title string, close []string) []Item {
	var out []Item
	normalized := Fold(strings.TrimSpace(title))
	for _, i := range s.Items {
		if Open(i) && (normalized != "" && Fold(strings.TrimSpace(i.Title)) == normalized || slices.Contains(close, i.ID)) {
			out = append(out, i)
		}
	}
	return out
}
