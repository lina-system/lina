// SPDX-License-Identifier: Apache-2.0
package intakereplay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/records"
)

const maxLine = 1024 * 1024

type located struct {
	Case
	line int
}

func Read(r io.Reader) ([]Case, []Result) {
	lines, rejected := read(r)
	cases := make([]Case, len(lines))
	for n, c := range lines {
		cases[n] = c.Case
	}
	return cases, rejected
}
func read(r io.Reader) ([]located, []Result) {
	reader := bufio.NewReader(r)
	var valid []located
	var rejected []Result
	seen := map[string]bool{}
	line := 0
	for {
		raw, err := caseLine(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			reason := "read_error"
			if errors.Is(err, errCaseTooLarge) {
				reason = "case_too_large"
			}
			rejected = append(rejected, Result{Line: line, Reason: reason})
			if errors.Is(err, errCaseTooLarge) {
				continue
			}
			break
		}
		var c Case
		reason := ""
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if !utf8.Valid(raw) || d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
			reason = "malformed_json"
		} else {
			reason = Validate(c)
		}
		if reason == "" && seen[c.ID] {
			reason = "duplicate_case"
		}
		if reason != "" {
			rejected = append(rejected, Result{Line: line, Reason: reason})
			continue
		}
		seen[c.ID] = true
		valid = append(valid, located{c, line})
	}
	if line == 0 && len(rejected) == 0 {
		rejected = append(rejected, Result{Reason: "empty_file"})
	}
	return valid, rejected
}

var errCaseTooLarge = errors.New("case_too_large")

// Drain only the oversized line so later cases remain independently countable.
func caseLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	large := false
	for {
		part, prefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(line)+len(part) > maxLine {
			large = true
		}
		if !large {
			line = append(line, part...)
		}
		if !prefix {
			if large {
				return nil, errCaseTooLarge
			}
			return line, nil
		}
	}
}

func Validate(c Case) string {
	if c.Version != 1 || strings.TrimSpace(c.ID) == "" || (c.Set != "random" && c.Set != "hard") || strings.TrimSpace(c.Input.Text) == "" || c.Input.PreviousTurnSeconds < 0 {
		return "invalid_case"
	}
	if len(c.Gold) == 0 {
		return "missing_gold"
	}
	s := c.State
	if s == nil {
		return "missing_state"
	}
	if s.Clock.IsZero() || s.FocusAgeSeconds < 0 || s.Items == nil || s.Questions == nil || s.Surfaces == nil {
		return "invalid_state"
	}
	items := map[string]Item{}
	for _, i := range s.Items {
		if i.ID == "" || strings.TrimSpace(i.Title) == "" || strings.ContainsAny(i.Title, "\r\n") || i.LastActivity.IsZero() || i.LastActivity.After(s.Clock) {
			return "invalid_item"
		}
		if _, ok := items[i.ID]; ok {
			return "duplicate_item"
		}
		switch i.Kind {
		case "todo":
			if i.State == "" || (records.Todo{State: i.State}).Validate() != nil {
				return "invalid_item"
			}
		case "initiative", "project":
			switch i.State {
			case "active", "open", "completed", "canceled", "closed":
			default:
				return "invalid_item"
			}
		case "task", "run":
			switch i.State {
			case "running", "waiting", "needs_input":
			default:
				return "invalid_item"
			}
		default:
			return "invalid_item"
		}
		items[i.ID] = i
	}
	targetOK := func(t Target) bool {
		switch t.Class {
		case "none":
			return t.ID == "" && t.Kind == "" && t.Parent == ""
		case "existing":
			i, ok := items[t.ID]
			return ok && t.Kind == i.Kind && t.Parent == ""
		case "new":
			if t.ID != "" || (t.Kind != "initiative" && t.Kind != "project" && t.Kind != "todo") {
				return false
			}
			if t.Parent == "" {
				return true
			}
			p, ok := items[t.Parent]
			return ok && (p.Kind == "initiative" || p.Kind == "project")
		}
		return false
	}
	if !targetOK(s.Focus) || s.Focus.Class == "new" {
		return "invalid_focus"
	}
	for _, g := range c.Gold {
		if !targetOK(g.Target) {
			return "invalid_gold"
		}
	}
	for _, b := range c.Observed {
		if !targetOK(b.Target) || b.Decider == "" {
			return "invalid_observed"
		}
	}
	questions := map[string]bool{}
	for _, q := range s.Questions {
		i, ok := items[q.Target]
		if !ok || !open(i) || q.ID == "" || questions[q.ID] {
			return "invalid_question"
		}
		questions[q.ID] = true
		labels := map[string]bool{}
		for _, v := range q.Options {
			v = fold(strings.TrimSpace(v))
			if v == "" || labels[v] {
				return "invalid_question"
			}
			labels[v] = true
		}
	}
	for key, id := range s.Surfaces {
		if _, ok := items[id]; key == "" || !ok {
			return "invalid_surface"
		}
	}
	for _, id := range []string{c.Input.ReplyTo, c.Input.Card, c.Input.Thread} {
		if id != "" && s.Surfaces[id] == "" {
			return "invalid_surface"
		}
	}
	for _, t := range c.Input.Recent {
		if (t.Role != "person" && t.Role != "agent") || strings.TrimSpace(t.Text) == "" {
			return "invalid_history"
		}
	}
	return ""
}

type Executor func(context.Context, Case, string) ([]Binding, error)

func File(ctx context.Context, path string, execute Executor) Report {
	f, err := os.Open(path)
	if err != nil {
		reason := "unreadable_file"
		if errors.Is(err, os.ErrNotExist) {
			reason = "missing_file"
		}
		return excludedReport(reason)
	}
	defer f.Close()
	return Run(ctx, f, execute)
}
func excludedReport(reason string) Report {
	return Report{Version: 1, Selected: 1, Excluded: 1, ExclusionReasons: map[string]int{reason: 1}, Results: []Result{{Reason: reason}}, Summaries: []Summary{}}
}
func Run(ctx context.Context, r io.Reader, execute Executor) Report {
	cases, rejected := read(r)
	out := Report{Version: 1, Selected: len(cases) + len(rejected), Excluded: len(rejected), ExclusionReasons: map[string]int{}, Results: rejected, Summaries: []Summary{}}
	for _, r := range rejected {
		out.ExclusionReasons[r.Reason]++
	}
	sums := map[string]*Summary{}
	totals := map[string]int{}
	add := func(c Case, router, decider string, m Metrics) {
		key := c.Set + "/" + router + "/" + decider
		if sums[key] == nil {
			sums[key] = &Summary{Set: c.Set, Router: router, Decider: decider}
		}
		sums[key].Metrics.add(m)
	}
	for _, entry := range cases {
		c := entry.Case
		results := []Result{}
		failed := false
		for _, router := range Routers {
			bindings, err := execute(ctx, c, router)
			if err != nil {
				failed = true
				break
			}
			m := Score(c, bindings)
			results = append(results, Result{Line: entry.line, ID: c.ID, Set: c.Set, Router: router, Bindings: bindings, Score: &m})
		}
		if failed {
			out.Excluded++
			out.ExclusionReasons["runtime_error"]++
			out.Results = append(out.Results, Result{Line: entry.line, ID: c.ID, Set: c.Set, Reason: "runtime_error"})
			continue
		}
		out.Scored++
		for _, r := range results {
			add(c, r.Router, "all", *r.Score)
			totals[c.Set+"/"+r.Router] += len(c.Gold)
			// Per-decider precision is per request, while the all row is per message.
			for i := 0; i < max(len(c.Gold), len(r.Bindings)); i++ {
				part := c
				part.Gold = nil
				var bs []Binding
				decider := "missing"
				if i < len(c.Gold) {
					part.Gold = []Gold{c.Gold[i]}
				}
				if i < len(r.Bindings) {
					bs = []Binding{r.Bindings[i]}
					decider = bs[0].Decider
				}
				add(c, r.Router, decider, Score(part, bs))
			}
		}
		out.Results = append(out.Results, results...)
	}
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		s := sums[key]
		m := s.Metrics
		den := totals[s.Set+"/"+s.Router]
		if s.Decider == "all" {
			den = m.Cases
		}
		s.Coverage = ratio(m.Cases, den)
		s.PassRate = ratio(m.Passed, m.Cases)
		s.RightItem = ratio(m.RightItem, m.Picks)
		s.NewOrExisting = ratio(m.ClassRight, m.Picks)
		s.QuestionRecall = ratio(m.AskedWarranted, m.Warranted)
		out.Summaries = append(out.Summaries, *s)
	}
	return out
}
