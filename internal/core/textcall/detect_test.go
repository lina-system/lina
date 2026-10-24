// SPDX-License-Identifier: Apache-2.0
package textcall

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestSyntheticDecisions(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range splitCases(data) {
		var c Case
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		t.Run(c.ID, func(t *testing.T) {
			got := Detect(c.Turn, c.DeclaredTools)
			if got.Detected != c.Gold.Detected || got.Action != c.Gold.Action || !equalCalls(got.Call, c.Gold.ParsedCall) {
				t.Fatalf("got %+v, want %+v", got, c.Gold)
			}
			if got.Detected && (got.VisibleText != "" || got.Action == Restart && got.Call != nil) {
				t.Fatalf("unsafe recovery: %+v", got)
			}
		})
	}
}

func BenchmarkDetect(b *testing.B) {
	tools := []Tool{{Name: "bash", Schema: json.RawMessage(`{"type":"object"}`)}}
	turn := Turn{StopReason: "stop", Text: []string{"[tool_use name=_bash]\n{\"command\":\"echo synthetic\"}"}}
	b.ResetTimer()
	for b.Loop() {
		Detect(turn, tools)
	}
}

func splitCases(data []byte) [][]byte { return bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) }
