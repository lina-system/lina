// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/internal/core/toolcatalog"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// The catalog is evidence, not executable bindings. Only matched successful
// load outputs in the replayed turn activate its declarations.
func recordedDeclarations(calls []Call) ([]provider.ToolDecl, error) {
	byName := map[string]provider.ToolDecl{}
	var order, legacy []string
	for _, call := range calls {
		if call.Call.Name != "load_tools" || call.Result == nil || call.Result.Code != loop.OK {
			continue
		}
		var loaded struct {
			Declarations []provider.ToolDecl `json:"declarations"`
			Order        []string            `json:"declaration_order"`
		}
		if json.Unmarshal(call.Result.Output, &loaded) != nil {
			return nil, ErrExcluded
		}
		if len(loaded.Order) > 0 {
			if order != nil && !slices.Equal(order, loaded.Order) {
				return nil, ErrExcluded
			}
			order = loaded.Order
		}
		for _, d := range loaded.Declarations {
			name := toolcatalog.Name(d)
			if name == "" {
				return nil, ErrExcluded
			}
			if prior, ok := byName[name]; ok {
				if string(prior) != string(d) {
					return nil, ErrExcluded
				}
			} else {
				byName[name] = d
				legacy = append(legacy, name)
			}
		}
	}
	// Older single-declaration loads have an unambiguous order. Multiple deferred
	// schemas without ordering evidence cannot reproduce the original prefix.
	if len(order) == 0 {
		if len(legacy) > 1 {
			return nil, ErrExcluded
		}
		order = legacy
	}
	var all []provider.ToolDecl
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			return nil, ErrExcluded
		}
		seen[name] = true
		if d := byName[name]; d != nil {
			all = append(all, d)
			delete(byName, name)
		}
	}
	if len(byName) > 0 {
		return nil, ErrExcluded
	}
	return all, nil
}
