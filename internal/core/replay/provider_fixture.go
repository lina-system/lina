// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/provider"
)

type ProviderFixture struct {
	Version int                 `json:"version"`
	Persona agents.Layers       `json:"persona"`
	Tools   []provider.ToolDecl `json:"tools"`
	History []provider.Item     `json:"history"`
	Turns   []FixtureTurn       `json:"turns"`
	// Summary adds one compaction summary request over the first Segment
	// history items, shaped as Core sends it. Absent means no summary row.
	Summary *FixtureSummary `json:"summary,omitempty"`
}

// FixtureSummary chooses the segment a measured summary request covers. The
// segment ends at a complete turn, so Segment is even and leaves a tail.
type FixtureSummary struct {
	Segment int `json:"segment"`
}
type FixtureTurn struct {
	Input   provider.Item `json:"input"`
	Context string        `json:"context"`
	Calls   []Call        `json:"calls"`
}

func ParseProviderFixture(raw string) (ProviderFixture, error) {
	var f ProviderFixture
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil || d.Decode(new(any)) != io.EOF || f.Version != 1 || len(f.History) != 80 || len(f.Turns) != 4 || len(f.Tools) == 0 || f.Persona.Core == "" {
		return f, ErrInvalid
	}
	names := map[string]bool{}
	for _, tool := range f.Tools {
		var d struct{ Type, Name string }
		if json.Unmarshal(tool, &d) != nil || d.Type != "function" || d.Name == "" || names[d.Name] {
			return f, ErrInvalid
		}
		names[d.Name] = true
	}
	for i, item := range f.History {
		var m struct{ Role string }
		if json.Unmarshal(item, &m) != nil || (i%2 == 0 && m.Role != "user") || (i%2 == 1 && m.Role != "assistant") {
			return f, ErrInvalid
		}
	}
	loops := 0
	for _, turn := range f.Turns {
		var m struct{ Role string }
		if json.Unmarshal(turn.Input, &m) != nil || m.Role != "user" || turn.Context == "" {
			return f, ErrInvalid
		}
		if len(turn.Calls) > 0 {
			loops++
		}
		for _, call := range turn.Calls {
			if !names[call.Call.Name] || !usable(call.Result) {
				return f, ErrInvalid
			}
			if _, err := callKey(call.Call); err != nil {
				return f, ErrInvalid
			}
		}
	}
	if loops != 2 {
		return f, ErrInvalid
	}
	if f.Summary != nil && (f.Summary.Segment <= 0 || f.Summary.Segment >= len(f.History) || f.Summary.Segment%2 != 0) {
		return f, ErrInvalid
	}
	return f, nil
}
