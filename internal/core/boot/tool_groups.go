// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/toolcatalog"
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
	initial := map[string]bool{}
	var names, order []string
	for _, d := range all {
		name := declarationName(d)
		byName[name] = d
		names = append(names, name)
		order = append(order, name)
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
		declarations := make([]provider.ToolDecl, 0, len(requested))
		for _, name := range requested {
			// Initial schemas are already in the request. Repeating that entire
			// catalog in tool history can crowd out the next useful operation.
			if !initial[name] {
				declarations = append(declarations, byName[name])
			}
		}
		raw, _ := json.Marshal(struct {
			Loaded       []string            `json:"loaded_tools"`
			Declarations []provider.ToolDecl `json:"declarations"`
			Order        []string            `json:"declaration_order"`
		}{requested, declarations, order})
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
	for _, d := range c.first {
		initial[declarationName(d)] = true
	}
	return c
}

func (c *turnToolCatalog) transform(ctx context.Context, r provider.Request) (provider.Request, error) {
	if c == nil {
		return r, nil
	}
	return (&toolcatalog.Catalog{First: c.first, All: c.all, Offset: c.offset}).Transform(ctx, r)
}
