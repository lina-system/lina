// SPDX-License-Identifier: Apache-2.0
package textcall

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Detect examines a completed turn against its active declarations. The caller
// must still send a recovered call through normal authorization and dispatch.
func Detect(turn Turn, tools []Tool) Decision {
	text := strings.Join(turn.Text, "")
	none := Decision{Action: None, VisibleText: text}
	if len(turn.ToolCalls) > 0 || !terminal(turn.StopReason) {
		return none
	}
	suffix := candidate(text)
	if suffix == "" {
		return none
	}
	restart := Decision{Detected: true, Action: Restart}
	if len(text) > MaxBytes || !utf8.ValidString(text) {
		return restart
	}
	call, trailingProse, err := parse(suffix)
	if trailingProse {
		return none
	}
	if err != nil {
		return restart
	}
	tool, ok := declared(call.Name, tools)
	if !ok {
		return restart
	}
	if err := call.arguments(tool.Schema); err != nil {
		return restart
	}
	args, err := decodeJSON(call.Arguments)
	if err != nil {
		return restart
	}
	if _, ok := args.(map[string]any); !ok {
		return restart
	}
	schema, err := compileSchema(tool.Schema)
	if err != nil || schema.Validate(args) != nil {
		return restart
	}
	call.Name = tool.Name
	call.Arguments, _ = json.Marshal(args)
	return Decision{Detected: true, Action: Dispatch, Call: &call.Call}
}
func terminal(reason string) bool {
	return reason == "stop" || reason == "end_turn" || reason == "length" || reason == "max_tokens"
}
func declared(name string, tools []Tool) (Tool, bool) {
	// An exact declared underscore name wins over normalization.
	for _, target := range []string{name, strings.TrimPrefix(name, "_")} {
		count := 0
		var found Tool
		for _, tool := range tools {
			if target != "" && tool.Name == target {
				found = tool
				count++
			}
		}
		if count > 0 {
			return found, count == 1
		}
	}
	return Tool{}, false
}

type localOnly struct{}

func (localOnly) Load(string) (any, error) { return nil, errSyntax }
func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	schema, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	compiler.UseLoader(localOnly{})
	if err := compiler.AddResource("https://textcall.invalid/arguments", schema); err != nil {
		return nil, err
	}
	return compiler.Compile("https://textcall.invalid/arguments")
}

var markers = []string{"[tool_use", "<tool_use", "<function_calls", "<invoke", "<antml:function_calls", "<antml:invoke", "<tool_call"}

func startsCall(s string) bool {
	for _, m := range markers {
		if strings.HasPrefix(s, m) && (len(s) == len(m) || strings.ContainsRune(" \t\r\n>]/", rune(s[len(m)]))) {
			return true
		}
	}
	return false
}

// Only standalone syntax can be an attempted call. Preserve the original bytes
// after finding it: masking Markdown inside arguments would change the call.
func candidate(text string) string {
	fence := byte(0)
	width := 0
	thinking := ""
	diff := false
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		start := offset
		offset += len(line)
		if thinking != "" {
			close := "</" + thinking + ">"
			end := strings.Index(line, close)
			if end < 0 {
				continue
			}
			start += end + len(close)
			line = line[end+len(close):]
			thinking = ""
		}
		trimmed := strings.TrimSpace(line)
		if fence != 0 {
			if len(trimmed) >= width && strings.Trim(trimmed, string(fence)) == "" {
				fence = 0
			}
			continue
		}
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		// Thinking tags are blocks only at the start of the visible line.
		// A literal tag mentioned in prose must not swallow the next call.
		for {
			tag := ""
			for _, name := range []string{"think", "thinking", "analysis"} {
				if strings.HasPrefix(trimmed, "<"+name+">") {
					tag = name
					break
				}
			}
			if tag == "" {
				break
			}
			close := "</" + tag + ">"
			end := strings.Index(line, close)
			if end < 0 {
				thinking = tag
				break
			}
			start += end + len(close)
			line = line[end+len(close):]
			trimmed = strings.TrimSpace(line)
		}
		if thinking != "" {
			continue
		}
		if len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				fence = trimmed[0]
				width = n
				continue
			}
		}
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "--- ") {
			diff = true
			continue
		}
		if diff {
			if trimmed == "" || strings.ContainsRune(" +-\\@", rune(line[0])) {
				continue
			}
			diff = false
		}
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if startsCall(trimmed) {
			indent := len(line) - len(strings.TrimLeft(line, " \t\r\n"))
			return strings.TrimSpace(text[start+indent:])
		}
	}
	return ""
}
