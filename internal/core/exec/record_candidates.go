// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type CandidateQuery struct {
	After int64 `json:"after"`
}

// Current is the authenticated turn's retained event, never a caller identity.
func RegisterRecordCandidates(registry *tools.Registry, store *records.Store, actor records.Actor, current string) ([]tools.Spec, error) {
	var specs []tools.Spec
	for _, name := range []string{"propose_candidate", "list_candidates"} {
		spec := tools.Spec{Name: name, Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Classify: func(in tools.Input) tools.EffectClass {
			if in.Write {
				return tools.PrivateDraft
			}
			return tools.Research
		}, Schema: func(raw json.RawMessage) (tools.Input, error) {
			in := tools.Input{Target: "records:query", What: name, Where: "Records", ScopeKey: "records"}
			if name == "list_candidates" {
				var a CandidateQuery
				if decodeSkill(raw, &a) != nil || a.After < 0 {
					return in, tools.ErrInvalid
				}
				in.Arguments, _ = json.Marshal(a)
			} else {
				var a records.CandidateInput
				if decodeSkill(raw, &a) != nil || len(a.Source) > 512 || len(a.Quotation) > 1<<20 || strings.TrimSpace(a.Title) == "" || len(a.Title) > 512 || strings.ContainsAny(a.Title, "\r\n") || len(a.Note) > 1<<20 || a.Confidence < 0 || a.Confidence > 1 {
					return in, tools.ErrInvalid
				}
				if a.Source == "" {
					a.Source = current
				}
				if a.Source == "" {
					return in, tools.ErrInvalid
				}
				in.Arguments, _ = json.Marshal(a)
				in.Target = "records:new"
				in.Write = true
				in.Undoable = true
			}
			return in, nil
		}}
		err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
			if name == "list_candidates" {
				var a CandidateQuery
				_ = json.Unmarshal(in.Arguments, &a)
				rows, err := store.Candidates(ctx, actor.Person, a.After)
				if err != nil {
					return RecordFailure(err), nil
				}
				return tools.Result{State: tools.Applied, Body: output(map[string]any{"candidates": rows})}, nil
			}
			var a records.CandidateInput
			_ = json.Unmarshal(in.Arguments, &a)
			c, err := store.ProposeCandidate(ctx, actor, a)
			if err != nil {
				return RecordFailure(err), nil
			}
			return tools.Result{State: tools.Applied, Body: output(c), Revision: strconv.FormatInt(c.Number, 10) + ":" + c.Status}, nil
		})
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
func candidateDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"propose_candidate","description":"Retain a possible to-do with an exact quotation and ledger event source (empty source means this person's current message). Core rechecks owner, speaker and quotation before adding anything. Use the action stated in the source as title. Confidence never authorizes an addition. Ambiguous or unavailable evidence stays pending. Returns a numbered candidate and, only after addition, its record and undo handle.","parameters":{"type":"object","properties":{"source":{"type":"string"},"quotation":{"type":"string"},"title":{"type":"string"},"note":{"type":"string"},"confidence":{"type":"number","minimum":0,"maximum":1}},"required":["source","quotation","title","note","confidence"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"list_candidates","description":"Read a retained page of numbered candidates, including pending and rejected candidates. Use the last number as after. State exactly which numbers an offer concerns; never interpret an unscoped add all as permission.","parameters":{"type":"object","properties":{"after":{"type":"integer","minimum":0}},"required":["after"],"additionalProperties":false}}`),
	}
}
