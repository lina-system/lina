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
			raw, _ := json.Marshal(r.Tools)
			r.Instructions = r.Instructions[:start] + string(raw) + r.Instructions[start+end:]
		}
	}
	return r, nil
}
