// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

// Only Core-owned names classify declarations. Model text and plugin-supplied
// descriptions cannot change group membership or grant authority.
func declarationGroup(name string) string {
	switch name {
	case "prepare_work", "retain_work_result", "report_work_result", "land_work_result", "dispatch_work", "dispatch_code_goal", "revise_code_goal", "worker_start", "worker_input", "worker_interrupt", "worker_snapshot", "worker_quota", "worker_read_file":
		return "work"
	case "create_timer", "timer_status", "cancel_timer":
		return "calendar"
	case "observe_health", "repair_records_index":
		return "device"
	case "web_search", "web_extract":
		return "web_research"
	}
	return "" // Core's always-declared set.
}
func declarationName(raw provider.ToolDecl) string {
	var d struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Name
}

type turnToolCatalog struct {
	all    []provider.ToolDecl
	first  []provider.ToolDecl
	offset int
}

func prepareToolGroups(all []provider.ToolDecl, method agents.WorkMethodSettings, bindings map[string]loop.Tool) *turnToolCatalog {
	c := &turnToolCatalog{all: slices.Clone(all)}
	byName := map[string]provider.ToolDecl{}
	var names []string
	for _, d := range all {
		name := declarationName(d)
		byName[name] = d
		names = append(names, name)
	}
	slices.Sort(names)
	decode := func(raw string) ([]string, error) {
		var in struct {
			Tools []string `json:"tools"`
		}
		if decodeWorkerArgs(json.RawMessage(raw), &in) != nil || len(in.Tools) == 0 || len(in.Tools) > len(names) {
			return nil, tools.ErrInvalid
		}
		seen := map[string]bool{}
		for _, name := range in.Tools {
			if byName[name] == nil || seen[name] {
				return nil, tools.ErrInvalid
			}
			seen[name] = true
		}
		return in.Tools, nil
	}
	bindings["load_tools"] = loop.Tool{Validate: func(call provider.ToolCall) (string, error) {
		_, err := decode(call.Arguments)
		return "tool-catalog", err
	}, Execute: func(_ context.Context, call provider.ToolCall) loop.Result {
		requested, err := decode(call.Arguments)
		if err != nil {
			return loop.Result{ID: call.ID, Code: loop.Invalid}
		}
		var declarations []provider.ToolDecl
		for _, name := range requested {
			declarations = append(declarations, byName[name])
		}
		raw, _ := json.Marshal(struct {
			Loaded       []string            `json:"loaded_tools"`
			Declarations []provider.ToolDecl `json:"declarations"`
		}{requested, declarations})
		return loop.Result{ID: call.ID, Code: loop.OK, Output: raw}
	}}
	loader, _ := json.Marshal(map[string]any{"type": "function", "name": "load_tools", "description": "Load available tools for this turn. Loading changes declarations only; each call still checks permissions.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"tools": map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": names}}}, "required": []string{"tools"}, "additionalProperties": false}})
	for _, d := range all {
		if declarationGroup(declarationName(d)) == "" {
			c.first = append(c.first, d)
		}
	}
	// Agent runtimes may hold their native declaration list fixed for a session.
	// This bridge invokes only the already-registered deferred bindings; their
	// own validation and execution/authority gates remain the final boundary.
	deferred := map[string]bool{}
	var deferredNames []string
	for _, name := range names {
		if declarationGroup(name) != "" {
			deferred[name] = true
			deferredNames = append(deferredNames, name)
		}
	}
	decodeCall := func(call provider.ToolCall) (provider.ToolCall, loop.Tool, error) {
		var in struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if decodeWorkerArgs(json.RawMessage(call.Arguments), &in) != nil || !bytes.HasPrefix(bytes.TrimSpace(in.Arguments), []byte("{")) {
			return provider.ToolCall{}, loop.Tool{}, tools.ErrInvalid
		}
		// Resolve the final binding: channel and completion policies may wrap
		// it after the declaration catalog is prepared.
		binding := bindings[in.Name]
		if !deferred[in.Name] || binding.Validate == nil || binding.Execute == nil {
			return provider.ToolCall{}, loop.Tool{}, tools.ErrInvalid
		}
		nested := provider.ToolCall{ID: call.ID, Name: in.Name, Arguments: string(in.Arguments)}
		return nested, binding, nil
	}
	bindings["call_tool"] = loop.Tool{Validate: func(call provider.ToolCall) (string, error) {
		nested, binding, err := decodeCall(call)
		if err != nil {
			return "", err
		}
		return binding.Validate(nested)
	}, Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
		nested, binding, err := decodeCall(call)
		if err != nil {
			return loop.Result{ID: call.ID, Code: loop.Invalid}
		}
		if _, err = binding.Validate(nested); err != nil {
			return loop.Result{ID: call.ID, Code: loop.Invalid}
		}
		return binding.Execute(ctx, nested)
	}}
	invoke, _ := json.Marshal(map[string]any{"type": "function", "name": "call_tool", "description": "Invoke a deferred tool using the schema returned by load_tools. Use this when the runtime cannot add native tool declarations during its session. The tool's validation and permissions still apply.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "enum": deferredNames}, "arguments": map[string]any{"type": "object"}}, "required": []string{"name", "arguments"}, "additionalProperties": false}})
	c.first = append(c.first, loader, invoke)
	var groups []string
	_ = json.Unmarshal(method.Values["tools.declare_first"], &groups)
	for _, g := range groups {
		for _, d := range all {
			if declarationGroup(declarationName(d)) == g {
				c.first = append(c.first, d)
			}
		}
	}
	return c
}

// Reconstruct on-demand declarations from committed tool outputs in this turn,
// not mutable callback state or a previous turn's history. This also works when
// the tool journal replays an already committed load result.
func (c *turnToolCatalog) transform(_ context.Context, r provider.Request) (provider.Request, error) {
	if c == nil {
		return r, nil
	}
	loaded := map[string]bool{}
	calls := map[string]bool{}
	for _, item := range r.Items[c.offset:] {
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
	r.Tools = slices.Clone(c.first)
	for _, d := range c.first {
		delete(loaded, declarationName(d))
	}
	for _, d := range c.all {
		if loaded[declarationName(d)] {
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
