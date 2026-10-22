// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

// RegisterGrowth exposes committed notices and their single undo action in an
// agent-bound registry. Growth adoption itself belongs to the Moirai job path.
func RegisterGrowth(registry *tools.Registry, store *agents.Store, agent, person string) ([]tools.Spec, error) {
	if registry == nil || store == nil || strings.TrimSpace(agent) == "" || strings.TrimSpace(person) == "" {
		return nil, tools.ErrInvalid
	}
	var specs []tools.Spec
	for _, name := range []string{"growth_notices", "undo_growth"} {
		write := name == "undo_growth"
		spec := tools.Spec{Classify: tools.Class(tools.PrivateDraft), Name: name, Placement: tools.Core, Conc: 1, Deadline: 10 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
			var args any = &struct{}{}
			if write {
				args = &agents.GrowthUndo{}
			}
			if err := decodeSkill(raw, args); err != nil {
				return tools.Input{}, err
			}
			if u, ok := args.(*agents.GrowthUndo); ok && u.Revision <= 0 {
				return tools.Input{}, tools.ErrInvalid
			}
			normalized, err := json.Marshal(args)
			if err != nil {
				return tools.Input{}, err
			}
			what := "Read growth notices"
			if write {
				what = "Undo a growth step"
			}
			return tools.Input{Arguments: normalized, Target: "agent:" + agent, What: what, Where: "Agent persona", ScopeKey: "growth", Write: write, Undoable: write}, nil
		}}
		handler := func(ctx context.Context, _ lease.Lease, input tools.Input) (tools.Result, error) {
			var value any
			var revision string
			var err error
			if write {
				var u agents.GrowthUndo
				if err = decodeSkill(input.Arguments, &u); err == nil {
					var a agents.Agent
					a, err = store.UndoGrowth(ctx, agent, person, u)
					value = a
					revision = strconv.FormatInt(a.Revision, 10)
				}
			} else {
				value, err = store.GrowthNotices(ctx, agent)
			}
			if err != nil {
				code := "growth_operation_failed"
				for _, known := range []error{agents.ErrInvalid, agents.ErrNotFound, agents.ErrConflict, agents.ErrGrowthUndone} {
					if errors.Is(err, known) {
						code = known.Error()
						break
					}
				}
				body, _ := json.Marshal(struct {
					Code string `json:"code"`
				}{code})
				return tools.Result{State: tools.Failed, Body: body}, nil
			}
			body, err := json.Marshal(value)
			if err != nil {
				return tools.Result{State: tools.Failed}, err
			}
			return tools.Result{State: tools.Applied, Body: body, Revision: revision}, nil
		}
		if err := registry.Register(spec, handler); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
