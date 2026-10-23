// SPDX-License-Identifier: Apache-2.0

// Package intakereplay defines private intake cases and deterministic baselines.
// It does not enable a binding policy for ordinary conversations.
package intakereplay

import "time"

var Routers = []string{"focus-only", "rules-then-focus"}

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
type Gold struct {
	Target Target `json:"target"`
	Ask    bool   `json:"ask"`
}
type Binding struct {
	Target     Target   `json:"target"`
	Ask        bool     `json:"ask"`
	Decider    string   `json:"decider"`
	Candidates []string `json:"candidates,omitempty"`
}
type Case struct {
	Version  int                `json:"version"`
	ID       string             `json:"id"`
	Set      string             `json:"set"`
	State    *State             `json:"state"`
	Input    Input              `json:"input"`
	Gold     []Gold             `json:"gold"`
	Observed []Binding          `json:"observed,omitempty"`
	Expected map[string]Metrics `json:"expected,omitempty"`
}
type Metrics struct {
	Cases              int            `json:"cases"`
	Passed             int            `json:"passed"`
	Requests           int            `json:"requests"`
	Picks              int            `json:"picks"`
	RightItem          int            `json:"right_item"`
	ClassRight         int            `json:"class_right"`
	Warranted          int            `json:"warranted"`
	AskedWarranted     int            `json:"asked_warranted"`
	NeedlessQuestions  int            `json:"needless_questions"`
	SilentWrong        int            `json:"silent_wrong"`
	RunningSilentWrong int            `json:"running_silent_wrong"`
	MissingBindings    int            `json:"missing_bindings"`
	ExtraBindings      int            `json:"extra_bindings"`
	Confusion          map[string]int `json:"silent_wrong_classes"`
}
type Ratio struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Rate        *float64 `json:"rate"` // null when no observation can establish a rate
}
type Summary struct {
	Set            string  `json:"set"`
	Router         string  `json:"router"`
	Decider        string  `json:"decider"`
	Metrics        Metrics `json:"counts"`
	Coverage       Ratio   `json:"coverage"`
	PassRate       Ratio   `json:"pass_rate"`
	RightItem      Ratio   `json:"right_item"`
	NewOrExisting  Ratio   `json:"new_or_existing"`
	QuestionRecall Ratio   `json:"questions_when_warranted"`
}
type Result struct {
	Line     int       `json:"line"`
	ID       string    `json:"id,omitempty"`
	Set      string    `json:"set,omitempty"`
	Router   string    `json:"router,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Bindings []Binding `json:"bindings,omitempty"`
	Score    *Metrics  `json:"score,omitempty"`
}
type Report struct {
	Version          int            `json:"version"`
	Selected         int            `json:"selected"`
	Scored           int            `json:"scored"`
	Excluded         int            `json:"excluded"`
	ExclusionReasons map[string]int `json:"exclusion_reasons"`
	Results          []Result       `json:"results"`
	Summaries        []Summary      `json:"summaries"`
}
