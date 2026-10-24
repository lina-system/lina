// SPDX-License-Identifier: Apache-2.0
package textcall

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

var slices = []string{"complete_allowed", "incomplete_or_disallowed", "normal_prose", "real_tool_present"}

type Ratio struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Rate        *float64 `json:"rate"`
}
type Scores struct {
	Cases               int   `json:"cases"`
	Matched             int   `json:"matched"`
	Recovered           Ratio `json:"recovered_call_accuracy"`
	FalsePositives      Ratio `json:"false_positive_rate"`
	HandlingTotalNS     int64 `json:"handling_total_ns"`
	HandlingMaxNS       int64 `json:"handling_max_ns"`
	Over5MS             int   `json:"handling_over_5ms"`
	SimulatedDispatches int   `json:"simulated_dispatches"`
	ExtraModelTurns     int   `json:"planned_extra_model_turns"`
	FreshSessionStarts  int   `json:"planned_fresh_session_starts"`
	SameSessionNudges   int   `json:"same_session_nudges"`
}
type Row struct {
	Line                int    `json:"line"`
	ID                  string `json:"id,omitempty"`
	Slice               string `json:"slice,omitempty"`
	Route               string `json:"route,omitempty"`
	Reason              string `json:"reason,omitempty"`
	Action              string `json:"action,omitempty"`
	Detected            bool   `json:"detected"`
	Matched             bool   `json:"matched"`
	HandlingNS          int64  `json:"handling_ns"`
	SimulatedDispatches int    `json:"simulated_dispatches"`
	ExtraModelTurns     int    `json:"planned_extra_model_turns"`
	FreshSessionStarts  int    `json:"planned_fresh_session_starts"`
}
type Report struct {
	Version          int               `json:"version"`
	Rejected         int               `json:"rejected"`
	RejectionReasons map[string]int    `json:"rejection_reasons"`
	Overall          Scores            `json:"overall"`
	Slices           map[string]Scores `json:"slices"`
	Results          []Row             `json:"results"`
}

func (r Report) Passed() bool {
	return r.Rejected == 0 && r.Overall.Cases > 0 && r.Overall.Matched == r.Overall.Cases
}
func newReport() Report {
	r := Report{Version: 1, RejectionReasons: map[string]int{}, Slices: map[string]Scores{}, Results: []Row{}}
	for _, slice := range slices {
		r.Slices[slice] = Scores{}
	}
	return r
}
func (r *Report) reject(line int, reason string) {
	r.Rejected++
	r.RejectionReasons[reason]++
	r.Results = append(r.Results, Row{Line: line, Reason: reason})
}

// ReplayFile only reads the supplied path. Neither input nor error contents
// are echoed, and no tool, model request or session is executed.
func ReplayFile(path string) Report {
	file, err := os.Open(path)
	if err != nil {
		r := newReport()
		r.reject(0, "file_unavailable")
		return r
	}
	defer file.Close()
	return Replay(file)
}
func Replay(input io.Reader) Report {
	report := newReport()
	reader := bufio.NewReader(input)
	seen := map[string]bool{}
	line := 0
	for {
		raw, reason, err := readLine(reader)
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			report.reject(line, "read_error")
			break
		}
		if reason != "" {
			report.reject(line, reason)
			continue
		}
		c, reason := readCase(raw)
		if reason == "" && seen[c.ID] {
			reason = "duplicate_case"
		}
		if reason != "" {
			report.reject(line, reason)
			continue
		}
		seen[c.ID] = true
		start := time.Now()
		decision := Detect(c.Turn, c.DeclaredTools)
		elapsed := time.Since(start).Nanoseconds()
		row := Row{Line: line, ID: c.ID, Slice: c.Slice, Route: c.Route, Action: decision.Action, Detected: decision.Detected, HandlingNS: elapsed}
		// These counters describe the proposed action at the pure boundary. They
		// are never evidence of actual tool effects or completed session starts.
		if decision.Action == Dispatch && decision.Call != nil {
			row.SimulatedDispatches = 1
		}
		if decision.Action == Restart {
			row.ExtraModelTurns = 1
			row.FreshSessionStarts = 1
		}
		row.Matched = decision.Detected == c.Gold.Detected && decision.Action == c.Gold.Action && equalCalls(decision.Call, c.Gold.ParsedCall) && (!c.Gold.Detected || decision.VisibleText == "")
		report.Results = append(report.Results, row)
		report.Overall = addScore(report.Overall, row)
		report.Slices[c.Slice] = addScore(report.Slices[c.Slice], row)
	}
	if line == 0 {
		report.reject(0, "empty_file")
	}
	return report
}
func addScore(score Scores, row Row) Scores {
	score.Cases++
	if row.Matched {
		score.Matched++
	}
	if row.Slice == "complete_allowed" || row.Slice == "incomplete_or_disallowed" {
		score.Recovered.Denominator++
		if row.Matched {
			score.Recovered.Numerator++
		}
	} else {
		score.FalsePositives.Denominator++
		if row.Detected {
			score.FalsePositives.Numerator++
		}
	}
	for _, ratio := range []*Ratio{&score.Recovered, &score.FalsePositives} {
		if ratio.Denominator > 0 {
			rate := float64(ratio.Numerator) / float64(ratio.Denominator)
			ratio.Rate = &rate
		}
	}
	score.HandlingTotalNS += row.HandlingNS
	if row.HandlingNS > score.HandlingMaxNS {
		score.HandlingMaxNS = row.HandlingNS
	}
	if row.HandlingNS >= int64(5*time.Millisecond) {
		score.Over5MS++
	}
	score.SimulatedDispatches += row.SimulatedDispatches
	score.ExtraModelTurns += row.ExtraModelTurns
	score.FreshSessionStarts += row.FreshSessionStarts
	return score
}

// Drain oversized lines and continue, so subsequent cases are still scored.
func readLine(reader *bufio.Reader) ([]byte, string, error) {
	var line []byte
	large := false
	for {
		part, prefix, err := reader.ReadLine()
		if err != nil {
			return nil, "", err
		}
		if len(line)+len(part) > MaxBytes {
			large = true
		}
		if !large {
			line = append(line, part...)
		}
		if !prefix {
			if large {
				return nil, "line_too_large", nil
			}
			if len(bytes.TrimSpace(line)) == 0 {
				return nil, "missing_line", nil
			}
			return line, "", nil
		}
	}
}
func readCase(raw []byte) (Case, string) {
	var c Case
	value, err := decodeJSON(raw)
	if err != nil {
		return c, "malformed_json"
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return c, "invalid_case"
	}
	if !has(obj, "id", "slice", "route", "declared_tools", "turn", "gold") {
		return c, "missing_fields"
	}
	turn, ok := obj["turn"].(map[string]any)
	if !ok || !has(turn, "stop_reason", "tool_calls", "text", "thinking", "delta_timestamps_ms") {
		return c, "missing_fields"
	}
	gold, ok := obj["gold"].(map[string]any)
	if !ok || !has(gold, "detected", "action", "progress_leaked") {
		return c, "missing_fields"
	}
	if gold["action"] == Dispatch && !has(gold, "parsed_call") {
		return c, "missing_fields"
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, "invalid_case"
	}
	if !validCase(c) {
		return c, "invalid_case"
	}
	return c, ""
}
func has(obj map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := obj[key]; !ok || value == nil {
			return false
		}
	}
	return true
}
func validCase(c Case) bool {
	if strings.TrimSpace(c.ID) == "" || !member(c.Slice, slices...) || !member(c.Route, "model_route", "acp_worker", "codex_worker") || c.DeclaredTools == nil || c.Turn.Text == nil || c.Turn.Thinking == nil || c.Turn.ToolCalls == nil || c.Turn.DeltaTimestampsMS == nil || c.Turn.StopReason == "" {
		return false
	}
	previous := int64(0)
	for _, stamp := range c.Turn.DeltaTimestampsMS {
		if stamp < previous {
			return false
		}
		previous = stamp
	}
	names := map[string]bool{}
	for _, tool := range c.DeclaredTools {
		if tool.Name == "" || strings.TrimSpace(tool.Name) != tool.Name || names[tool.Name] {
			return false
		}
		names[tool.Name] = true
		if _, err := compileSchema(tool.Schema); err != nil {
			return false
		}
	}
	for _, call := range c.Turn.ToolCalls {
		value, err := decodeJSON(call)
		if err != nil {
			return false
		}
		obj, ok := value.(map[string]any)
		if !ok || !has(obj, "name", "arguments") {
			return false
		}
		name, ok := obj["name"].(string)
		if !ok || name == "" {
			return false
		}
		if _, ok := obj["arguments"].(map[string]any); !ok {
			return false
		}
	}
	if c.Gold.ProgressLeaked {
		return false
	}
	switch c.Slice {
	case "complete_allowed":
		if !c.Gold.Detected || c.Gold.Action != Dispatch || c.Gold.ParsedCall == nil {
			return false
		}
		call := c.Gold.ParsedCall
		tool, ok := declared(call.Name, c.DeclaredTools)
		if !ok || tool.Name != call.Name {
			return false
		}
		value, err := decodeJSON(call.Arguments)
		if err != nil {
			return false
		}
		if _, ok := value.(map[string]any); !ok {
			return false
		}
		schema, err := compileSchema(tool.Schema)
		if err != nil || schema.Validate(value) != nil {
			return false
		}
	case "incomplete_or_disallowed":
		if !c.Gold.Detected || c.Gold.Action != Restart || c.Gold.ParsedCall != nil {
			return false
		}
	case "normal_prose", "real_tool_present":
		if c.Gold.Detected || c.Gold.Action != None || c.Gold.ParsedCall != nil {
			return false
		}
	}
	if c.Gold.Detected && (!terminal(c.Turn.StopReason) || len(c.Turn.ToolCalls) > 0) {
		return false
	}
	return (c.Slice == "real_tool_present") == (len(c.Turn.ToolCalls) > 0)
}
func member(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
