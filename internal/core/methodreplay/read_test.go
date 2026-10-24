// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestReadSyntheticCases(t *testing.T) {
	f, err := os.Open("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cases, rejected := Read(f)
	if len(rejected) != 0 {
		t.Fatalf("rejected=%+v", rejected)
	}
	if len(cases) < 6 {
		t.Fatalf("cases=%d", len(cases))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.ID] {
			t.Fatalf("duplicate case %s", c.ID)
		}
		seen[c.ID] = true
		if Validate(c) != "" {
			t.Fatalf("case %s invalid: %s", c.ID, Validate(c))
		}
	}
}

func TestReadRejectsInvalidCases(t *testing.T) {
	valid := `{"version":1,"id":"dev-001","role":"dev","reply_type":"status","kind":"correction","correction":"length","checks":["length"],"budget":120,"turns":[{"role":"user","text":"상태 알려줘"}]}`
	edit := func(change func(map[string]any)) string {
		var c map[string]any
		if err := json.Unmarshal([]byte(valid), &c); err != nil {
			t.Fatal(err)
		}
		change(c)
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	cases := []struct {
		name, line, reason string
	}{
		{"malformed", "{", "malformed_json"},
		{"unknown field", strings.Replace(valid, `"version":1`, `"version":1,"extra":true`, 1), "malformed_json"},
		{"wrong version", edit(func(c map[string]any) { c["version"] = 2 }), "invalid_case"},
		{"empty id", edit(func(c map[string]any) { c["id"] = "" }), "invalid_case"},
		{"unknown role", edit(func(c map[string]any) { c["role"] = "nobody" }), "invalid_case"},
		{"unknown kind", edit(func(c map[string]any) { c["kind"] = "maybe" }), "invalid_case"},
		{"correction class missing", edit(func(c map[string]any) { delete(c, "correction") }), "invalid_case"},
		{"no checks", edit(func(c map[string]any) { c["checks"] = []string{} }), "invalid_case"},
		{"unknown check", edit(func(c map[string]any) { c["checks"] = []string{"tone"} }), "invalid_case"},
		{"length without budget", edit(func(c map[string]any) { delete(c, "budget") }), "invalid_case"},
		{"question on a non-reversible step", edit(func(c map[string]any) {
			c["checks"] = []string{"no_question"}
		}), "invalid_case"},
		{"language without a language", edit(func(c map[string]any) { c["checks"] = []string{"language"} }), "invalid_case"},
		{"no turns", edit(func(c map[string]any) { c["turns"] = []map[string]any{} }), "invalid_case"},
		{"last turn is not the person", edit(func(c map[string]any) {
			c["turns"] = []map[string]any{{"role": "user", "text": "상태"}, {"role": "assistant", "text": "이전 답"}}
		}), "invalid_case"},
		{"only injected turns", edit(func(c map[string]any) {
			c["turns"] = []map[string]any{{"role": "user", "text": "요약", "injected": true}}
		}), "invalid_case"},
		{"unknown turn role", edit(func(c map[string]any) {
			c["turns"] = []map[string]any{{"role": "system", "text": "지시"}}
		}), "invalid_case"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, rejected := Read(strings.NewReader(tc.line))
			if len(rejected) != 1 || rejected[0].Reason != tc.reason {
				t.Fatalf("rejected=%+v; want %s", rejected, tc.reason)
			}
		})
	}
}

func TestReadRejectsDuplicateIDsAndEmptyInput(t *testing.T) {
	valid := `{"version":1,"id":"dev-001","role":"dev","reply_type":"status","kind":"accepted","checks":["no_internal"],"turns":[{"role":"user","text":"상태 알려줘"}]}`
	cases, rejected := Read(strings.NewReader(valid + "\n" + valid + "\n"))
	if len(cases) != 1 || len(rejected) != 1 || rejected[0].Reason != "duplicate_case" {
		t.Fatalf("cases=%d rejected=%+v", len(cases), rejected)
	}
	cases, rejected = Read(strings.NewReader(""))
	if len(cases) != 0 || len(rejected) != 1 || rejected[0].Reason != "empty_file" {
		t.Fatalf("empty input: %+v", rejected)
	}
}

// The M4-1 numbers warn that injected user turns skew asking and length scores.
func TestDroppedTurns(t *testing.T) {
	c := Case{Turns: []Turn{
		{Role: "user", Text: "지난 대화 요약입니다", Injected: true},
		{Role: "user", Text: "배포 상태 알려줘"},
		{Role: "assistant", Text: "확인했습니다"},
		{Role: "user", Text: "You are a delegated worker. Brief: ship it", Injected: false},
		{Role: "user", Text: "그럼 지금 해줘"},
	}}
	kept, dropped := Dropped(c)
	if dropped != 2 {
		t.Fatalf("dropped=%d", dropped)
	}
	var texts []string
	for _, turn := range kept {
		texts = append(texts, turn.Text)
	}
	if !reflect.DeepEqual(texts, []string{"배포 상태 알려줘", "확인했습니다", "그럼 지금 해줘"}) {
		t.Fatalf("kept=%v", texts)
	}
}
