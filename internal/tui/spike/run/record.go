// SPDX-License-Identifier: Apache-2.0

// Package run supports the opt-in terminal experiments, built with go test -c.
package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/tui/spike"
)

var scenarioIDs = []string{"S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9", "S10", "S11"}

func boundsFor(id string) map[string]any {
	switch id {
	case "S1":
		return map[string]any{"cursor_matches_expected": true, "composition_in_place": true}
	case "S2":
		return map[string]any{"max_tick_bytes": 2048, "columns": 140, "rows": 45}
	case "S3":
		return map[string]any{"build_p50_ms": 4, "build_p95_ms": 16, "flush_p50_ms": 4, "flush_p95_ms": 16, "flat_per_flush_cost": true, "peak_heap_mib": 64, "key_to_byte_p95_ms": 50, "events_per_second": 250}
	case "S4":
		return map[string]any{"reflow_after_coalesce_ms": 100, "tab_alignment": true, "history_messages": 200, "coalesce_ms": 32}
	case "S5":
		return map[string]any{"resolve_ms": 200, "reply_leaked": false}
	case "S6":
		return map[string]any{"press_gate_correct": true, "esc_to_action_ms": 80}
	case "S7":
		return map[string]any{"later_key_ms": 1000, "gap_requires_mitigation": true}
	case "S8":
		return map[string]any{"runs": true, "quits": true, "parses": true, "sleep_calls": 0}
	case "S9":
		return map[string]any{"cpu_percent_one_core": 1, "idle_bytes": 0, "resident_mib": 80, "window_items": 200}
	case "S10":
		return map[string]any{"both_width_modes_align": true, "own_tables_on_glamour_miss": true}
	case "S11":
		return map[string]any{"minimum_columns": 100, "maximum_columns": 140, "default_columns": 120}
	default:
		return nil
	}
}

type assessment struct {
	values map[string]any
	failed bool
	err    error
}

func (a *assessment) number(key string) float64 {
	v, ok := a.values[key].(float64)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		a.err = fmt.Errorf("missing or invalid measurement: %s", key)
		return 0
	}
	return v
}
func (a *assessment) flag(key string) bool {
	v, ok := a.values[key].(bool)
	if !ok {
		a.err = fmt.Errorf("missing boolean observation: %s", key)
	}
	return v
}
func (a *assessment) text(key string) string {
	v, ok := a.values[key].(string)
	if !ok || strings.TrimSpace(v) == "" {
		a.err = fmt.Errorf("missing observation: %s", key)
	}
	return v
}
func (a *assessment) atMost(key string, bound float64) {
	if a.number(key) > bound {
		a.failed = true
	}
}
func (a *assessment) equals(key string, value float64) {
	if a.number(key) != value {
		a.failed = true
	}
}
func (a *assessment) require(key string) {
	if !a.flag(key) {
		a.failed = true
	}
}

// verdict never fills in a missing measurement or treats an unrun case as a gap.
func verdict(id string, values map[string]any) (string, error) {
	a := assessment{values: values}
	switch id {
	case "S1":
		for _, c := range []string{"composed", "midline", "wrapped"} {
			a.require(c + "_cursor_matches")
			a.require(c + "_composition_in_place")
		}
	case "S2":
		a.atMost("max_tick_bytes", 2048)
		a.equals("columns", 140)
		a.equals("rows", 45)
	case "S3":
		for _, f := range streamFixtures() {
			p := f.id + "_"
			a.atMost(p+"build_p50_ms", 4)
			a.atMost(p+"build_p95_ms", 16)
			a.atMost(p+"flush_p50_ms", 4)
			a.atMost(p+"flush_p95_ms", 16)
			a.atMost(p+"peak_heap_mib", 64)
			a.atMost(p+"key_to_byte_p95_ms", 50)
			a.equals(p+"target_events_per_second", 250)
			a.require(p + "flat_per_flush_cost")
			if a.number(p+"key_samples") < 20 || a.number(p+"flush_samples") < 20 {
				a.failed = true
			}
		}
	case "S4":
		a.atMost("reflow_max_ms", 100)
		a.require("tab_alignment")
		a.equals("history_messages", 200)
		if a.number("resize_samples") < 3 {
			a.failed = true
		}
	case "S5":
		for _, p := range []string{"sync", "grapheme", "osc52", "osc8"} {
			a.atMost(p+"_resolve_ms", 200)
			a.flag(p + "_supported")
		}
		if a.flag("reply_leaked") {
			a.failed = true
		}
		a.require("late_reply_quarantined")
	case "S6":
		a.require("press_gate_correct")
		a.atMost("esc_to_action_max_ms", 80)
		if a.number("esc_samples") < 5 {
			a.failed = true
		}
		a.flag("event_types_supported")
	case "S7":
		worked, ms := a.flag("later_key_worked"), a.number("observed_ms")
		if !worked || ms > 1000 {
			a.text("mitigation")
			if !worked && ms < 1000 {
				a.err = errors.New("lost-paste observation ended before its one-second bound")
			}
			if a.err != nil {
				return "", a.err
			}
			return "gap", nil
		}
	case "S8":
		a.require("runs")
		a.require("quits")
		a.require("parses")
		a.equals("sleep_calls", 0)
	case "S9":
		a.atMost("cpu_percent_one_core", 1)
		a.equals("idle_bytes", 0)
		a.atMost("peak_resident_mib", 80)
		a.equals("window_items", 200)
	case "S10":
		if !a.flag("grapheme_supported") {
			a.flag("glamour_wcwidth_aligned")
			a.flag("own_wcwidth_aligned")
			a.text("mitigation")
			if a.err != nil {
				return "", a.err
			}
			return "fail", nil
		}
		for _, mode := range []string{"wcwidth", "grapheme"} {
			glamour := a.flag("glamour_" + mode + "_aligned")
			a.require("own_" + mode + "_aligned")
			if !glamour {
				a.require("own_tables_selected")
			}
		}
	case "S11":
		if !a.flag("threshold_found") {
			for width := 100; width <= 140; width++ {
				if a.flag(fmt.Sprintf("width_%d_usable", width)) {
					a.err = errors.New("a usable width was observed but no threshold was selected")
				}
			}
			a.text("threshold_reason")
			if a.err != nil {
				return "", a.err
			}
			return "fail", nil
		}
		threshold := a.number("threshold_columns")
		a.text("threshold_reason")
		if threshold < 100 || threshold > 140 || threshold != math.Trunc(threshold) {
			a.failed = true
		}
		for _, w := range []int{100, 120, 140} {
			a.flag(fmt.Sprintf("width_%d_usable", w))
		}
		a.require(fmt.Sprintf("width_%d_usable", int(threshold)))
		if threshold > 100 && a.flag(fmt.Sprintf("width_%d_usable", int(threshold)-1)) {
			a.failed = true
		}
	default:
		return "", errors.New("unknown scenario")
	}
	if a.err != nil {
		return "", a.err
	}
	if a.failed {
		return "fail", nil
	}
	return "pass", nil
}

func rawMetrics(values map[string]any) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		result[key] = data
	}
	return result
}

func validateRecord(record spike.Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	var values map[string]any
	data, err := json.Marshal(record.Measured)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	want, err := verdict(record.Scenario, values)
	if err != nil {
		return err
	}
	if want != record.Result {
		return errors.New("result does not match measured values")
	}
	wantBounds, _ := json.Marshal(rawMetrics(boundsFor(record.Scenario)))
	gotBounds, _ := json.Marshal(record.Bounds)
	if string(wantBounds) != string(gotBounds) {
		return errors.New("declared bounds differ from the pre-run issue")
	}
	return nil
}

func saveRecord(path string, record spike.Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".spike-evidence-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Link publishes a complete record atomically and refuses an existing path.
	return os.Link(f.Name(), path)
}

func checkEvidence(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	paths := []string{path}
	if info.IsDir() {
		paths, err = filepath.Glob(filepath.Join(path, "*.json"))
		if err != nil {
			return 0, err
		}
	}
	if len(paths) == 0 {
		return 0, errors.New("no scenario records found")
	}
	for _, name := range paths {
		f, err := os.Open(name)
		if err != nil {
			return 0, err
		}
		record, err := spike.ReadRecord(f)
		f.Close()
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		if err := validateRecord(record); err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
	}
	return len(paths), nil
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	copy := slices.Clone(values)
	slices.Sort(copy)
	return copy[max(0, min(len(copy)-1, int(math.Ceil(p*float64(len(copy))))-1))]
}

type streamFixture struct{ id, text string }

func streamFixtures() []streamFixture {
	fill := func(tile string, size int) string {
		return strings.Repeat(tile, size/len(tile)) + strings.Repeat("x", size%len(tile))
	}
	var fence, table, list strings.Builder
	fence.WriteString("```go\n")
	table.WriteString("| 이름 | value |\n| --- | --- |\n")
	for i := 0; i < 1200; i++ {
		if i < 1000 {
			fmt.Fprintf(&fence, "value%d := %d\n", i, i)
		}
		fmt.Fprintf(&table, "| 항목%d | %d |\n", i, i)
		if i < 300 {
			fmt.Fprintf(&list, "- 항목 %d synthetic text\n", i)
		}
	}
	fence.WriteString("```\n")
	return []streamFixture{{"prose", fill("한글 synthetic prose.\n\n", 100000)}, {"no_blank", fill("한글 synthetic text with a line ending.\n", 300000)}, {"no_newline", fill("한글 synthetic text ", 300000)}, {"fence", fence.String()}, {"table", table.String()}, {"list", list.String()}}
}
