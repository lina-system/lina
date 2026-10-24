// SPDX-License-Identifier: Apache-2.0

// Package binding ranks authorized snapshots and applies surface-fact rules.
package binding

import "time"

type Target struct {
	Class  string `json:"class"`
	ID     string `json:"id,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Parent string `json:"parent,omitempty"`
}
type Item struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	LastActivity time.Time `json:"last_activity"`
	Names        []string  `json:"names,omitempty"`
	Links        []string  `json:"links,omitempty"`
	StartedHere  bool      `json:"-"`
}
type Question struct {
	ID      string   `json:"id"`
	Target  string   `json:"target"`
	Options []string `json:"options"`
	YesNo   bool     `json:"yes_no,omitempty"`
}
type State struct {
	Clock           time.Time  `json:"clock"`
	Focus           Target     `json:"focus"`
	FocusAgeSeconds int64      `json:"focus_age_seconds"`
	Items           []Item     `json:"items"`
	Questions       []Question `json:"questions"`
	// Surface keys identify reply messages, cards or threads; values are item ids.
	Surfaces map[string]string `json:"surfaces"`
}
type Turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type Input struct {
	Text                string `json:"text"`
	ReplyTo             string `json:"reply_to,omitempty"`
	Card                string `json:"card,omitempty"`
	Thread              string `json:"thread,omitempty"`
	PreviousTurnSeconds int64  `json:"previous_turn_seconds"`
	Recent              []Turn `json:"recent"`
}
type Decision struct {
	Target     Target `json:"target"`
	Candidates []Item `json:"candidates"`
	Rule       string `json:"rule,omitempty"`
	Decider    string `json:"decider"`
	Unresolved bool   `json:"unresolved"`
	FocusFresh bool   `json:"focus_fresh"`
	Snapshot   *State `json:"snapshot,omitempty"`
}
