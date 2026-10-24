// SPDX-License-Identifier: Apache-2.0

// Package methodreplay scores the M4-1 replay set against the untailored and
// tailored presets. It reads private cases, plays one turn per case and variant,
// and reports only scores and check outcomes: case and reply text never enter a
// report, so a report can be committed.
package methodreplay

import "github.com/thisisjun786/lina/internal/core/agents"

// Check names one mechanical expectation of the design's measurement section.
type Check string

const (
	Length       Check = "length"
	NoQuestion   Check = "no_question"
	NoInternal   Check = "no_internal"
	DoneEvidence Check = "done_evidence"
	Language     Check = "language"
)

var checks = []Check{Length, NoQuestion, NoInternal, DoneEvidence, Language}

func (c Check) known() bool {
	for _, k := range checks {
		if c == k {
			return true
		}
	}
	return false
}

var replyTypes = []string{"status", "comparison", "research", "document", "message", "candidates"}

var corrections = []string{"length", "asking", "naming", "done_claim", "other"}

// Turn is one recorded message of the case's conversation. Injected marks a
// user-role turn the system wrote (a compaction summary or a delegated brief).
type Turn struct {
	Role     string `json:"role"`
	Text     string `json:"text"`
	Injected bool   `json:"injected,omitempty"`
}

// Case is one private replay case: a real owner request with the reply he
// accepted or the correction he gave, plus the checks it must satisfy.
type Case struct {
	Version        int      `json:"version"`
	ID             string   `json:"id"`
	Role           string   `json:"role"`
	ReplyType      string   `json:"reply_type"`
	Kind           string   `json:"kind"`
	Correction     string   `json:"correction,omitempty"`
	Origin         string   `json:"origin,omitempty"`
	Checks         []Check  `json:"checks"`
	Budget         int      `json:"budget,omitempty"`
	Language       string   `json:"language,omitempty"`
	RetiredAliases []string `json:"retired_aliases,omitempty"`
	Reversible     bool     `json:"reversible,omitempty"`
	MustNotChange  bool     `json:"must_not_change,omitempty"`
	Turns          []Turn   `json:"turns"`
}

// Rejection names a case line that was left out, by line and reason only.
type Rejection struct {
	Line   int    `json:"line"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// Preset is one agent's persona layers and work-method record as a variant
// carries them.
type Preset struct {
	Layers     agents.Layers
	WorkMethod string
}

// Variant is one preset set: the snapshot each agent's cases are played with.
// The baseline variant usually carries the shipped layers and the empty record.
type Variant struct {
	Name    string
	Presets map[string]Preset
}

// For returns the snapshot one role is played with.
func (v Variant) For(role string) (Preset, bool) {
	preset, ok := v.Presets[role]
	return preset, ok
}

// CheckResult is one check's outcome for one reply.
type CheckResult struct {
	Check  Check `json:"check"`
	Passed bool  `json:"passed"`
}

// Result is one reply's score. Chars counts non-space characters, the Korean
// unit the person's reply budgets use.
type Result struct {
	Passed bool          `json:"passed"`
	Chars  int           `json:"chars"`
	Checks []CheckResult `json:"checks"`
}

// CheckTally counts passed runs of one check.
type CheckTally struct {
	Check  Check `json:"check"`
	Passes int   `json:"passes"`
}

// VariantResult holds one variant's runs of one case. Reason is a failure code
// such as model_error; it never contains provider text.
type VariantResult struct {
	Name   string       `json:"name"`
	Runs   int          `json:"runs"`
	Passes int          `json:"passes"`
	Chars  int          `json:"chars"`
	Checks []CheckTally `json:"checks"`
	Reason string       `json:"reason,omitempty"`
}

// CaseResult holds both variants' results for one case.
type CaseResult struct {
	ID            string          `json:"id"`
	Role          string          `json:"role"`
	Kind          string          `json:"kind"`
	Correction    string          `json:"correction,omitempty"`
	MustNotChange bool            `json:"must_not_change,omitempty"`
	Variants      []VariantResult `json:"variants"`
}

// AgentSummary is the per-agent before and after. Regressions lists the case ids
// that passed before and fail after.
type AgentSummary struct {
	Agent          string   `json:"agent"`
	Cases          int      `json:"cases"`
	BaselineRuns   int      `json:"baseline_runs"`
	BaselinePasses int      `json:"baseline_passes"`
	TailoredRuns   int      `json:"tailored_runs"`
	TailoredPasses int      `json:"tailored_passes"`
	Regressions    []string `json:"regressions,omitempty"`
	Improvements   []string `json:"improvements,omitempty"`
	Unchanged      int      `json:"unchanged"`
	Verdict        string   `json:"verdict"`
}

// Ship is the design's release rule: a change ships only if it improves its
// agent and regresses no other observed agent. Unobserved names starters that
// had no cases, so their scores establish nothing.
type Ship struct {
	Changed    []string `json:"changed"`
	Ships      bool     `json:"ships"`
	Reasons    []string `json:"reasons,omitempty"`
	Unobserved []string `json:"unobserved,omitempty"`
}

// Report is the measurement. It holds ids, check outcomes and counts only.
type Report struct {
	Version          int            `json:"version"`
	Selected         int            `json:"selected"`
	Scored           int            `json:"scored"`
	Excluded         int            `json:"excluded"`
	ExclusionReasons map[string]int `json:"exclusion_reasons"`
	DroppedTurns     int            `json:"dropped_turns"`
	Rejected         []Rejection    `json:"rejected,omitempty"`
	Variants         []string       `json:"variants"`
	Baseline         string         `json:"baseline"`
	Repeat           int            `json:"repeat"`
	Cases            []CaseResult   `json:"cases"`
	Agents           []AgentSummary `json:"agents"`
	Ship             Ship           `json:"ship"`
}
