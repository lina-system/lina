// SPDX-License-Identifier: Apache-2.0
package texttool

import (
	"encoding/json"
	"reflect"
	"testing"
)

func tools() []Declaration {
	return []Declaration{
		{Name: "bash", Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"integer"}},"required":["command"]}`)},
		{Name: "read", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
		{Name: "edit", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)},
		{Name: "submit_receipt", Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"},"count":{"type":"integer"},"items":{"type":"array"}},"required":["ok"]}`)},
	}
}

func TestCompleteCalls(t *testing.T) {
	cases := []struct{ name, text, tool, args string }{
		{"bracket", "[tool_use id=one name=_bash]\n{\"command\":\"printf ok\"}", "bash", `{"command":"printf ok"}`},
		{"xml-json", `<tool_use id="one" name="_read">{"path":"a.txt"}</tool_use>`, "read", `{"path":"a.txt"}`},
		{"xml-children", `<tool_use name="_edit"><path>a.txt</path><content>hello &amp; world</content></tool_use>`, "edit", `{"path":"a.txt","content":"hello & world"}`},
		{"invoke", `<function_calls><invoke name="_bash"><parameter name="command">echo ok</parameter><parameter name="timeout">10</parameter></invoke></function_calls>`, "bash", `{"command":"echo ok","timeout":10}`},
		{"antml", `<antml:invoke name="_submit_receipt"><antml:parameter name="ok">true</antml:parameter><antml:parameter name="count">9007199254740993</antml:parameter><antml:parameter name="items">[1,"x"]</antml:parameter></antml:invoke>`, "submit_receipt", `{"ok":true,"count":9007199254740993,"items":[1,"x"]}`},
		{"wrapper", `<tool_call>{"arguments":{"path":"a.txt"},"name":"_read"}</tool_call>`, "read", `{"path":"a.txt"}`},
		{"preface", "I will inspect the file.\n<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"a.txt\"}}</tool_call>", "read", `{"path":"a.txt"}`},
		{"multiline-string", "[tool_use name=bash]\n{\"command\":\"echo '<invoke name=x>'\"}", "bash", `{"command":"echo '<invoke name=x>'"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(Turn{StopReason: "stop", Text: tc.text}, tools())
			if !got.Detected || got.Action != ParseAndDispatch || got.ParsedCall == nil {
				t.Fatalf("decision = %+v", got)
			}
			if got.ParsedCall.Name != tc.tool {
				t.Fatalf("name = %q", got.ParsedCall.Name)
			}
			var a, b any
			json.Unmarshal(got.ParsedCall.Arguments, &a)
			json.Unmarshal([]byte(tc.args), &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("arguments = %s, want %s", got.ParsedCall.Arguments, tc.args)
			}
			if got.VisibleText != "" || got.ExtraTurns != 0 || got.SameSessionNudges != 0 || got.FreshSessionStarts != 0 {
				t.Fatalf("effects = %+v", got)
			}
		})
	}
}

func TestUnsafeCallsRestart(t *testing.T) {
	for _, text := range []string{
		`[tool_use`, `[tool_use name=_bash]`, "[tool_use name=bash]\n{\"command\":",
		"[tool_use name=bash]\n{\"command\":\"a\",\"command\":\"b\"}",
		`[tool_use name=bash] null`, `[tool_use name=__bash] {"command":"x"}`, `[tool_use name=unknown] {}`,
		`[tool_use name=bash name=read] {"command":"x"}`, `[tool_use name=bash] {"command":NaN}`,
		`<tool_use name="read">{"path":"a"}`, `<tool_call>{"name":"read","arguments":{"path":`,
		`<tool_call>{"name":"read","arguments":null}</tool_call>`,
		`<tool_call>{"name":"read","name":"bash","arguments":{}}</tool_call>`,
		`<invoke name="bash"><parameter name="command">x</parameter>`,
		`<invoke name="bash"><parameter name="command">x</parameter><parameter name="command">y</parameter></invoke>`,
		`<invoke name="bash"><parameter name="timeout">oops</parameter></invoke>`,
		`<invoke name="bash"><parameter name="unknown">x</parameter></invoke>`,
		`<function_calls><invoke name="bash">{"command":"x"}</invoke><invoke name="read">{"path":"a"}</invoke></function_calls>`,
		`<antml:invoke name="bash"><parameter name="command">x</parameter></antml:invoke>`,
		`<tool_use name="read"><path><nested>ambiguous</nested></path></tool_use>`,
		`<tool_use name="read"><path>&unknown;</path></tool_use>`,
		`<tool_call>{"name":"read","arguments":{}}</tool_call><tool_call>{"name":"read","arguments":{}}</tool_call>`,
		"[tool_use name=read] {\"path\":\"a\"}\n[tool_use name=bash] {\"command\":\"x\"}",
	} {
		t.Run(text, func(t *testing.T) {
			got := Detect(Turn{StopReason: "end_turn", Text: text}, tools())
			if !got.Detected || got.Action != RestartFreshSession || got.ParsedCall != nil || got.VisibleText != "" || got.FreshSessionStarts != 1 || got.ExtraTurns != 1 || got.SameSessionNudges != 0 {
				t.Fatalf("decision = %+v", got)
			}
		})
	}
}

func TestNonCalls(t *testing.T) {
	call := `<invoke name="bash"><parameter name="command">echo ok</parameter></invoke>`
	for _, text := range []string{
		"Work is complete.", "Use [tool_use or <invoke> for examples.", "`" + call + "`",
		"```xml\n" + call + "\n```", "~~~\n[tool_use name=bash]\n{\"command\":\"x\"}\n~~~",
		"```xml\n" + call, "    " + call, "\t" + call, "> " + call,
		"diff --git a/example b/example\n@@ -1 +1 @@\n-" + call + "\n+" + call,
		"<think>\n" + call + "\n</think>\nDone.", "<thinking>" + call + "</thinking>",
		call + "\nThis is an example, not a request.", "[tool_use] is a marker in the log.", "<invoke> is an XML tag.",
	} {
		t.Run(text, func(t *testing.T) {
			got := Detect(Turn{StopReason: "stop", Text: text}, tools())
			if got.Detected || got.Action != None || got.ParsedCall != nil || got.VisibleText != text {
				t.Fatalf("decision = %+v", got)
			}
		})
	}
	for _, turn := range []Turn{
		{Text: call}, {StopReason: "tool_calls", Text: call},
		{StopReason: "stop", Text: call, ToolCalls: []Call{{Name: "bash", Arguments: json.RawMessage(`{}`)}}},
		{StopReason: "stop", Thinking: []string{call}, Text: "Done."},
	} {
		if got := Detect(turn, tools()); got.Action != None || got.Detected {
			t.Fatalf("decision = %+v", got)
		}
	}
}

func TestExactNameWinsAndOnlyOneUnderscore(t *testing.T) {
	ds := append(tools(), Declaration{Name: "_bash", Schema: json.RawMessage(`{"type":"object"}`)})
	got := Detect(Turn{StopReason: "stop", Text: `[tool_use name=_bash] {"command":"x"}`}, ds)
	if got.ParsedCall == nil || got.ParsedCall.Name != "_bash" {
		t.Fatalf("decision = %+v", got)
	}
	got = Detect(Turn{StopReason: "stop", Text: `[tool_use name=__bash] {"command":"x"}`}, ds)
	if got.ParsedCall == nil || got.ParsedCall.Name != "_bash" {
		t.Fatalf("one underscore decision = %+v", got)
	}
}
