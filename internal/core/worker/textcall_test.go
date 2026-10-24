// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/textcall"
)

func TestTurnTextReplacesDeltasAndBoundsMemory(t *testing.T) {
	var b TurnText
	b.Add("one", "[tool", false)
	b.Add("one", "_use", false)
	b.Add("one", "complete", true)
	b.Add("two", "second", true)
	if b.Text() != "complete\nsecond" {
		t.Fatal(b.Text())
	}
	b.Add("two", strings.Repeat("x", textcall.MaxBytes), true)
	if !b.Overflow || b.Text() != "" || len(b.parts) != 0 {
		t.Fatal("overflow retained text")
	}
	b.Add("one", "late", true)
	if b.Text() != "" {
		t.Fatal("overflow revived")
	}
}
func TestTextCallUsesOnlyActiveWorkTools(t *testing.T) {
	tools := WorkToolDeclarations()
	tools = append(tools, Tool{Namespace: "lina", Name: "bash", InputSchema: []byte(`{"type":"object"}`)})
	if got := DetectTextCall(`[tool_use id=x name=_bash] {}`, "end_turn", false, tools); got.Action != textcall.Restart {
		t.Fatal(got)
	}
	if got := DetectTextCall(`[tool_use id=x name=_check_inbox] {}`, "end_turn", false, nil); got.Action != textcall.Restart {
		t.Fatal(got)
	}
	if got := DetectTextCall(`[tool_use id=x name=_check_inbox] {}`, "end_turn", false, tools); got.Action != textcall.Dispatch {
		t.Fatal(got)
	}
}
