// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"context"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/persona"
)

// Config is one measurement run. Play performs one case's conversation for one
// variant and returns only the reply text; the runner never stores it.
type Config struct {
	Cases    []Case
	Variants []Variant
	Play     func(context.Context, Case, Variant) (string, error)
	// Repeat is the number of observed runs per case and variant. One is the
	// minimum; the design scores only runs that happened.
	Repeat int
	// Changed names the agents whose tailoring this run measures.
	Changed []string
	// Baseline names what the baseline variant holds, for the report header.
	Baseline string
}

// baseline is the variant name compared against for regressions.
const baseline = "baseline"

// ErrConfig reports an unusable measurement configuration.
var ErrConfig = errors.New("invalid_measurement_config")

// Run plays every case under every variant and summarizes it per agent. A case
// that cannot be played is excluded with a reason code and never counted as a
// pass; its text never enters the report.
func Run(ctx context.Context, cfg Config) (Report, error) {
	if len(cfg.Variants) < 2 || cfg.Play == nil || cfg.Repeat < 0 {
		return Report{}, ErrConfig
	}
	repeat := cfg.Repeat
	if repeat == 0 {
		repeat = 1
	}
	names := make([]string, 0, len(cfg.Variants))
	for _, v := range cfg.Variants {
		names = append(names, v.Name)
	}
	report := Report{Version: 1, Selected: len(cfg.Cases), ExclusionReasons: map[string]int{}, Variants: names, Baseline: cfg.Baseline, Repeat: repeat}
	for _, c := range cfg.Cases {
		_, dropped := Dropped(c)
		report.DroppedTurns += dropped
		result := CaseResult{ID: c.ID, Role: c.Role, Kind: c.Kind, Correction: c.Correction, MustNotChange: c.MustNotChange}
		failed := ""
		for _, v := range cfg.Variants {
			runs := VariantResult{Name: v.Name, Checks: make([]CheckTally, 0, len(c.Checks))}
			for _, check := range c.Checks {
				runs.Checks = append(runs.Checks, CheckTally{Check: check})
			}
			short := ""
			for range repeat {
				reply, err := cfg.Play(ctx, c, v)
				if err != nil {
					if short == "" {
						short = failureReason(err)
					}
					if failed == "" {
						failed = short
					}
					continue
				}
				scored := Score(c, reply)
				runs.Runs++
				runs.Chars += scored.Chars
				if scored.Passed {
					runs.Passes++
				}
				for j, check := range scored.Checks {
					if check.Passed {
						runs.Checks[j].Passes++
					}
				}
			}
			if short != "" {
				// A case with any failed run is excluded, so the side that lost a
				// run names the reason even when its other runs completed.
				runs.Reason = short
			}
			result.Variants = append(result.Variants, runs)
		}
		if failed != "" {
			report.Excluded++
			report.ExclusionReasons[failed]++
		} else {
			report.Scored++
		}
		report.Cases = append(report.Cases, result)
	}
	report.Agents = summarize(report.Cases, cfg.Variants, repeat)
	report.Ship = ship(cfg.Changed, report.Agents, repeat)
	return report, nil
}

// failureReason keeps provider or runner errors out of the report. The caller's
// error text may carry paths or provider bodies, so only a stable code remains.
func failureReason(err error) string {
	if errors.Is(err, ErrConfig) {
		return "missing_preset"
	}
	return "model_error"
}

// summarize builds one row per agent in the order its first case appears. A case
// that is excluded, or that completed fewer than the configured runs on either
// side, is left out: a partial case is not a comparison.
func summarize(cases []CaseResult, variants []Variant, repeat int) []AgentSummary {
	order := []string{}
	byAgent := map[string]*AgentSummary{}
	for _, c := range cases {
		s := byAgent[c.Role]
		if s == nil {
			s = &AgentSummary{Agent: c.Role}
			byAgent[c.Role] = s
			order = append(order, c.Role)
		}
		if !complete(c, repeat) {
			continue
		}
		s.Cases++
		var before, after *VariantResult
		for i := range c.Variants {
			if c.Variants[i].Name == baseline {
				before = &c.Variants[i]
			}
			if c.Variants[i].Name != baseline {
				after = &c.Variants[i]
			}
		}
		if before == nil || after == nil {
			continue
		}
		s.BaselineRuns += before.Runs
		s.BaselinePasses += before.Passes
		s.TailoredRuns += after.Runs
		s.TailoredPasses += after.Passes
		switch {
		case before.Passes == before.Runs && after.Passes < after.Runs:
			s.Regressions = append(s.Regressions, c.ID)
		case before.Passes < before.Runs && after.Passes == after.Runs:
			s.Improvements = append(s.Improvements, c.ID)
		default:
			s.Unchanged++
		}
	}
	out := make([]AgentSummary, 0, len(order))
	for _, name := range order {
		s := byAgent[name]
		switch {
		case s.BaselineRuns == 0 || s.TailoredRuns == 0:
			s.Verdict = "insufficient"
		case len(s.Regressions) > 0:
			s.Verdict = "regressed"
		case s.TailoredPasses > s.BaselinePasses:
			s.Verdict = "improved"
		case s.TailoredPasses < s.BaselinePasses:
			s.Verdict = "worse"
		default:
			s.Verdict = "unchanged"
		}
		out = append(out, *s)
	}
	return out
}

// complete reports whether every variant produced all its runs, so both sides of
// a case rest on the same number of observed replies.
func complete(c CaseResult, repeat int) bool {
	for _, v := range c.Variants {
		if v.Reason != "" || v.Runs != repeat {
			return false
		}
	}
	return true
}

// ship applies the design's rule. It names why a change must not ship so the
// finding can go back to M4-2 instead of a silent revert. A ship needs the
// configured three repeats; fewer runs can still be scored, but they do not decide.
func ship(changed []string, agents []AgentSummary, repeat int) Ship {
	result := Ship{Changed: append([]string(nil), changed...)}
	if len(changed) == 0 {
		result.Reasons = []string{"no changed agent named"}
		return result
	}
	observed := map[string]bool{}
	for _, a := range agents {
		observed[a.Agent] = true
	}
	for _, s := range persona.Starters() {
		if !observed[s.Key] {
			result.Unobserved = append(result.Unobserved, s.Key)
		}
	}
	for _, agent := range changed {
		var found *AgentSummary
		for i := range agents {
			if agents[i].Agent == agent {
				found = &agents[i]
			}
		}
		if found == nil {
			result.Reasons = append(result.Reasons, agent+": no cases")
			continue
		}
		if found.Verdict != "improved" {
			result.Reasons = append(result.Reasons, agent+": "+found.Verdict)
		}
	}
	for _, a := range agents {
		if contains(changed, a.Agent) {
			continue
		}
		if len(a.Regressions) > 0 {
			result.Reasons = append(result.Reasons, a.Agent+": regressed other agent")
		}
	}
	if repeat < 3 {
		result.Reasons = append(result.Reasons, "fewer than three repeats")
	}
	result.Ships = len(result.Reasons) == 0
	return result
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TrimTrailing is the reply text the checks score: trailing whitespace carries
// no meaning, and the length unit ignores interior spaces.
func TrimTrailing(text string) string { return strings.TrimRight(text, " \t\r\n") }
