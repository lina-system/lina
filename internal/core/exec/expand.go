// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

// ExpandRead is supplied by the composition root. Its implementation must check
// the bound agent's ownership and live retractions before returning exact data.
// Keeping this function boundary avoids an import between Core flow packages.
type ExpandRead func(context.Context, string, string, int, int) (json.RawMessage, error)

// ExpandTool expands a summary node or ledger event across this agent's
// conversations. No model argument can change its agent or read scope.
func ExpandTool(agent string, read ExpandRead) (tools.Spec, tools.Handler) {
	type args struct {
		ID     string `json:"id"`
		Offset int    `json:"offset,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	decode := func(raw json.RawMessage) (args, error) {
		var a args
		if decodeMemoryArgs(raw, &a) != nil {
			return a, tools.ErrInvalid
		}
		if a.Limit == 0 {
			a.Limit = 50
		}
		if strings.TrimSpace(a.ID) == "" || a.Offset < 0 || a.Limit < 1 || a.Limit > 1000 {
			return a, tools.ErrInvalid
		}
		return a, nil
	}
	spec := tools.Spec{Name: "expand", Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
		a, err := decode(raw)
		if err != nil || agent == "" || read == nil {
			return tools.Input{}, tools.ErrInvalid
		}
		canonical, _ := json.Marshal(a)
		return tools.Input{Arguments: canonical, Target: "ledger/" + agent, ScopeKey: "ledger/" + agent, What: "Read original conversation evidence", Where: "Conversation"}, nil
	}}
	handler := func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		a, err := decode(in.Arguments)
		if err != nil || agent == "" || read == nil {
			return tools.Result{State: tools.Failed}, tools.ErrInvalid
		}
		body, err := read(ctx, agent, a.ID, a.Offset, a.Limit)
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		if !json.Valid(body) {
			return tools.Result{State: tools.Failed}, tools.ErrInvalid
		}
		return tools.Result{State: tools.Applied, Body: body}, nil
	}
	return spec, handler
}
