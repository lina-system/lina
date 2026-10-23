// SPDX-License-Identifier: Apache-2.0
// Package toolcatalog reconstructs turn declarations without executing tools.
package toolcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type Catalog struct {
	First, All []provider.ToolDecl
	Offset     int
}

func Name(raw provider.ToolDecl) string {
	var d struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Name
}

// EnabledNames identifies the native declarations without repeating their
// descriptions and schemas in the instruction prefix.
func EnabledNames(declarations []provider.ToolDecl) ([]byte, error) {
	names := make([]string, 0, len(declarations))
	for _, raw := range declarations {
		var declaration struct{ Name, Type string }
		if err := json.Unmarshal(raw, &declaration); err != nil {
			return nil, err
		}
		name := declaration.Name
		if name == "" {
			name = declaration.Type // Provider-owned built-in tools use their type.
		}
		names = append(names, name)
	}
	return json.Marshal(names)
}

// Reconstruct on-demand declarations from committed tool outputs in this turn,
// not mutable callback state or a previous turn's history. This also works when
// the tool journal replays an already committed load result.
func (c *Catalog) Transform(_ context.Context, r provider.Request) (provider.Request, error) {
	if c == nil {
		return r, nil
	}
	if c.Offset < 0 || c.Offset > len(r.Items) {
		return r, errors.New("tool_catalog_history_truncated")
	}
	loaded := map[string]bool{}
	calls := map[string]bool{}
	for _, item := range r.Items[c.Offset:] {
		var wire struct{ Type, Name, CallID, Output string }
		// CallID uses the provider's snake-case spelling.
		var id struct {
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(item, &wire) != nil || json.Unmarshal(item, &id) != nil {
			continue
		}
		if wire.Type == "function_call" && wire.Name == "load_tools" {
			calls[id.CallID] = true
		}
		if wire.Type != "function_call_output" || !calls[id.CallID] {
			continue
		}
		var result struct {
			Code   loop.Code `json:"code"`
			Output struct {
				Loaded []string `json:"loaded_tools"`
			} `json:"output"`
		}
		if json.Unmarshal([]byte(wire.Output), &result) != nil || result.Code != loop.OK {
			continue
		}
		for _, name := range result.Output.Loaded {
			loaded[name] = true
		}
	}
	r.Tools = slices.Clone(c.First)
	for _, d := range c.First {
		delete(loaded, Name(d))
	}
	for _, d := range c.All {
		if loaded[Name(d)] {
			r.Tools = append(r.Tools, d)
		}
	}
	if start := strings.Index(r.Instructions, "<enabled_tools>\n"); start >= 0 {
		start += len("<enabled_tools>\n")
		if end := strings.Index(r.Instructions[start:], "\n</enabled_tools>"); end >= 0 {
			raw, err := EnabledNames(r.Tools)
			if err != nil {
				return r, err
			}
			// Older recorded requests carry complete declarations in this block.
			// Retain their shape when reconstructing a replayed request.
			var previous []json.RawMessage
			if json.Unmarshal([]byte(r.Instructions[start:start+end]), &previous) == nil && len(previous) > 0 && strings.HasPrefix(strings.TrimSpace(string(previous[0])), "{") {
				raw, err = json.Marshal(r.Tools)
				if err != nil {
					return r, err
				}
			}
			r.Instructions = r.Instructions[:start] + string(raw) + r.Instructions[start+end:]
		}
	}
	return r, nil
}
