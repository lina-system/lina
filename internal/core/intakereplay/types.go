// SPDX-License-Identifier: Apache-2.0

// Package intakereplay defines private intake cases and deterministic baselines.
// It does not enable a binding policy for ordinary conversations.
package intakereplay

import "github.com/thisisjun786/lina/internal/core/binding"

var Routers = []string{"focus-only", "rules-then-focus"}

type Target = binding.Target
type Item = binding.Item
type Question = binding.Question
type State = binding.State
type Turn = binding.Turn
type Input = binding.Input
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
