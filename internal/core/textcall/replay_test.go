// SPDX-License-Identifier: Apache-2.0
package textcall

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestReplaySynthetic(t *testing.T) {
	report := ReplayFile("testdata/synthetic.jsonl")
	if !report.Passed() {
		t.Fatalf("replay failed: %+v", report)
	}
	if report.Overall.Recovered.Rate == nil || *report.Overall.Recovered.Rate != 1 || report.Overall.FalsePositives.Rate == nil || *report.Overall.FalsePositives.Rate != 0 {
		t.Fatalf("scores: %+v", report.Overall)
	}
	for _, slice := range slices {
		if report.Slices[slice].Cases == 0 {
			t.Fatalf("missing slice %s", slice)
		}
	}
	for _, row := range report.Results {
		if row.Slice == "incomplete_or_disallowed" && (row.SimulatedDispatches != 0 || row.FreshSessionStarts != 1 || row.ExtraModelTurns != 1) {
			t.Fatalf("unsafe restart: %+v", row)
		}
		if row.Slice == "complete_allowed" && (row.SimulatedDispatches != 1 || row.ExtraModelTurns != 0) {
			t.Fatalf("delayed dispatch: %+v", row)
		}
	}
}
func TestReplayInvalidLinesAreNotPasses(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	valid := string(splitCases(data)[0])
	missing := strings.Replace(valid, `"progress_leaked":false,`, "", 1)
	if missing == valid {
		missing = strings.Replace(valid, `,"progress_leaked":false`, "", 1)
	}
	input := strings.Join([]string{"", `{`, `{}`, missing, valid, valid, strings.Repeat("x", MaxBytes+1), strings.Replace(valid, `bracket-bash`, `after-large`, 1)}, "\n")
	report := Replay(strings.NewReader(input))
	if report.Passed() || report.Overall.Cases != 2 || report.Rejected != 6 {
		t.Fatalf("false green: %+v", report)
	}
	for _, reason := range []string{"missing_line", "malformed_json", "missing_fields", "duplicate_case", "line_too_large"} {
		if report.RejectionReasons[reason] == 0 {
			t.Fatalf("missing reason %s: %+v", reason, report)
		}
	}
}
func TestReplayEmptyAndUnreadable(t *testing.T) {
	for _, report := range []Report{Replay(strings.NewReader("")), ReplayFile(t.TempDir() + "/absent"), Replay(brokenReader{})} {
		if report.Passed() || report.Rejected != 1 || report.Overall.Cases != 0 || report.Overall.Recovered.Rate != nil {
			t.Fatalf("false green: %+v", report)
		}
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("private content must not leak") }
func TestReplayWrongGoldAndMissingFields(t *testing.T) {
	raw, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	original := splitCases(raw)[0]
	var c Case
	if err := json.Unmarshal(original, &c); err != nil {
		t.Fatal(err)
	}
	c.Gold.ParsedCall.Arguments = json.RawMessage(`{"command":"different"}`)
	data, _ := json.Marshal(c)
	report := Replay(bytes.NewReader(data))
	if report.Passed() || report.Overall.Matched != 0 || report.Overall.Recovered.Numerator != 0 {
		t.Fatalf("wrong gold passed: %+v", report)
	}
	for _, path := range [][]string{{"id"}, {"slice"}, {"route"}, {"declared_tools"}, {"turn"}, {"gold"}, {"turn", "stop_reason"}, {"turn", "tool_calls"}, {"turn", "text"}, {"turn", "thinking"}, {"turn", "delta_timestamps_ms"}, {"gold", "detected"}, {"gold", "action"}, {"gold", "progress_leaked"}, {"gold", "parsed_call"}} {
		var obj map[string]any
		if err := json.Unmarshal(original, &obj); err != nil {
			t.Fatal(err)
		}
		target := obj
		for _, key := range path[:len(path)-1] {
			target = target[key].(map[string]any)
		}
		delete(target, path[len(path)-1])
		data, _ := json.Marshal(obj)
		result := Replay(bytes.NewReader(data))
		if result.Passed() || result.Rejected != 1 {
			t.Errorf("missing %v passed: %+v", path, result)
		}
	}
}
func TestReplayPartialReadIsFailure(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	r := io.MultiReader(bytes.NewReader(append(splitCases(data)[0], '\n')), brokenReader{})
	report := Replay(r)
	if report.Passed() || report.Overall.Cases != 1 || report.RejectionReasons["read_error"] != 1 {
		t.Fatalf("lost read error: %+v", report)
	}
}

func TestReplayInvalidCaseReasons(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	original := splitCases(data)[0]
	changes := map[string]func(map[string]any){
		"unknown field":       func(c map[string]any) { c["typo"] = true },
		"unknown route":       func(c map[string]any) { c["route"] = "unknown" },
		"unknown slice":       func(c map[string]any) { c["slice"] = "unknown" },
		"decreasing deltas":   func(c map[string]any) { c["turn"].(map[string]any)["delta_timestamps_ms"] = []int{2, 1} },
		"inconsistent action": func(c map[string]any) { c["gold"].(map[string]any)["action"] = Restart },
		"leaked progress":     func(c map[string]any) { c["gold"].(map[string]any)["progress_leaked"] = true },
		"external schema": func(c map[string]any) {
			c["declared_tools"].([]any)[0].(map[string]any)["schema"] = map[string]any{"$ref": "https://unavailable.invalid/schema"}
		},
		"duplicate declaration": func(c map[string]any) { v := c["declared_tools"].([]any); c["declared_tools"] = append(v, v[0]) },
		"null call":             func(c map[string]any) { c["turn"].(map[string]any)["tool_calls"] = []any{nil} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			var c map[string]any
			if err := json.Unmarshal(original, &c); err != nil {
				t.Fatal(err)
			}
			change(c)
			raw, _ := json.Marshal(c)
			report := Replay(bytes.NewReader(raw))
			if report.Passed() || report.RejectionReasons["invalid_case"] != 1 {
				t.Fatalf("invalid case: %+v", report)
			}
		})
	}
	duplicate := bytes.Replace(original, []byte(`"id":`), []byte(`"id":"duplicate","id":`), 1)
	if r := Replay(bytes.NewReader(duplicate)); r.RejectionReasons["malformed_json"] != 1 {
		t.Fatalf("duplicate JSON: %+v", r)
	}
}
func TestCanonicalArguments(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{`{"x":1,"y":[true,null]}`, `{"y":[true,null],"x":1.00e0}`, true},
		{`{"x":9007199254740992}`, `{"x":9007199254740993}`, false},
		{`{"x":null}`, `{"y":null}`, false},
	} {
		a := &Call{Name: "tool", Arguments: json.RawMessage(tc.a)}
		b := &Call{Name: "tool", Arguments: json.RawMessage(tc.b)}
		if got := equalCalls(a, b); got != tc.equal {
			t.Fatalf("%s == %s: %v", tc.a, tc.b, got)
		}
	}
}
func TestSyntheticCoverage(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, line := range splitCases(data) {
		var c Case
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		ids[c.ID] = true
	}
	for _, family := range []string{"bracket", "xml_tool_use", "function_calls", "antml", "json_wrapper"} {
		for _, tool := range []string{"bash", "read", "edit", "submit_receipt"} {
			for _, prefix := range []string{"", "underscore-"} {
				if !ids[family+"-"+prefix+tool] {
					t.Errorf("missing %s %s%s coverage", family, prefix, tool)
				}
			}
		}
		for _, kind := range []string{"disallowed", "incomplete"} {
			if !ids[family+"-"+kind] {
				t.Errorf("missing %s %s coverage", family, kind)
			}
		}
	}
}
