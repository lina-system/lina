// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type healthObservation struct {
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Revision   string    `json:"revision"`
	Head       string    `json:"head"`
	IndexHead  string    `json:"index_head"`
	Records    int       `json:"records"`
	Texts      int       `json:"texts"`
	SearchRows int       `json:"search_rows"`
	Healthy    bool      `json:"healthy"`
}

func (r *conversationRuntime) observeRecordsHealth(ctx context.Context) (healthObservation, error) {
	h := healthObservation{Source: "core:records_index"}
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT s.head,i.head,(SELECT count(*) FROM records_index),(SELECT count(*) FROM records_text),(SELECT count(*) FROM records_fts) FROM records_state s JOIN records_index_state i ON i.singleton=s.singleton WHERE s.singleton=1`).Scan(&h.Head, &h.IndexHead, &h.Records, &h.Texts, &h.SearchRows)
	})
	if err != nil {
		return h, err
	}
	h.Healthy = h.Head == h.IndexHead && h.Records == h.Texts && h.Texts == h.SearchRows
	raw, _ := json.Marshal(h)
	hash := sha256.Sum256(raw)
	h.Revision = hex.EncodeToString(hash[:])
	h.ObservedAt = time.Now().UTC()
	return h, nil
}

func methodologyToolSchema(name string, raw json.RawMessage) (tools.Input, error) {
	var args any
	switch name {
	case "calculate":
		var a struct {
			Expression string `json:"expression"`
		}
		if decodeWorkerArgs(raw, &a) != nil {
			return tools.Input{}, tools.ErrInvalid
		}
		if _, err := coreexec.Calculate(a.Expression); err != nil {
			return tools.Input{}, err
		}
		args = a
	case "observe_health":
		if decodeWorkerArgs(raw, &struct{}{}) != nil {
			return tools.Input{}, tools.ErrInvalid
		}
		args = struct{}{}
	case "repair_records_index":
		var a struct {
			Revision string `json:"revision"`
		}
		if decodeWorkerArgs(raw, &a) != nil || len(a.Revision) != 64 {
			return tools.Input{}, tools.ErrInvalid
		}
		args = a
	default:
		return tools.Input{}, tools.ErrInvalid
	}
	canonical, _ := json.Marshal(args)
	target := "health:records_index"
	if name == "calculate" {
		target = "calculation:private"
	}
	return tools.Input{Arguments: canonical, Target: target, What: name, Where: target, ScopeKey: target, Write: name == "repair_records_index", Undoable: true}, nil
}

func (r *conversationRuntime) prepareMethodologyTools(turnID string, bindings map[string]loop.Tool) error {
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	for _, name := range []string{"calculate", "observe_health", "repair_records_index"} {
		class := tools.Research
		if name == "calculate" {
			class = tools.Calculation
		}
		if name == "repair_records_index" {
			class = tools.LocalWrite
		}
		spec := tools.Spec{Name: name, Classify: tools.Class(class), Placement: tools.Core, Conc: 1, Deadline: 30 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) { return methodologyToolSchema(name, raw) }}
		if err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
			if name == "calculate" {
				var a struct {
					Expression string `json:"expression"`
				}
				_ = json.Unmarshal(in.Arguments, &a)
				value, err := coreexec.Calculate(a.Expression)
				if err != nil {
					return tools.Result{State: tools.Failed}, err
				}
				raw, _ := json.Marshal(map[string]string{"expression": a.Expression, "exact_value": value})
				return tools.Result{State: tools.Applied, Body: raw}, nil
			}
			if name == "repair_records_index" {
				if err := r.records.Rebuild(ctx); err != nil {
					return tools.Result{State: tools.Failed}, err
				}
			}
			h, err := r.observeRecordsHealth(ctx)
			if err != nil {
				return tools.Result{State: tools.Failed}, err
			}
			raw, _ := json.Marshal(h)
			return tools.Result{State: tools.Applied, Body: raw, Revision: h.Revision}, nil
		}); err != nil {
			return err
		}
		validated := turn.Tool(registry, spec, tools.Call{}, turnID)
		bindings[name] = loop.Tool{Validate: validated.Validate, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			in, err := spec.Schema(json.RawMessage(c.Arguments))
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Invalid}
			}
			auth := grant.Request{GrantID: turn.EffectKey(turnID, name, in.Target, c.ID) + ":methodology", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "methodology"}
			g := grant.Grant{Identity: auth, Expires: time.Now().Add(time.Minute), Scope: []grant.Permission{{Tool: name, Target: in.Target, Write: in.Write}}}
			if in.Write {
				var a struct {
					Revision string `json:"revision"`
				}
				_ = json.Unmarshal(in.Arguments, &a)
				auth.Revision = a.Revision
				g.Identity = auth
				g.Revisions = map[string]string{in.Target: auth.Revision}
			}
			r.personaMu.Lock()
			r.personaGrants[auth.GrantID] = g
			r.personaMu.Unlock()
			defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
			return turn.Tool(registry, spec, tools.Call{Auth: auth}, turnID).Execute(ctx, c)
		}}
	}
	return nil
}

func methodologyDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		provider.ToolDecl(`{"type":"function","name":"calculate","description":"Calculate private decimal arithmetic exactly, without commands or network. Supports parentheses and + - * /. Returns a rational value and the expression as evidence.","parameters":{"type":"object","properties":{"expression":{"type":"string"}},"required":["expression"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"observe_health","description":"Observe Core's records index head and row-count consistency with source and time. This diagnoses only that component, not overall system health. Observe again after a repair before reporting recovery.","parameters":{"type":"object","properties":{},"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"repair_records_index","description":"Rebuild only Core's disposable records search index from its canonical records. Supply the revision from observe_health. Preserves user records and their history; does not grant administrator access. Verify with a fresh observation.","parameters":{"type":"object","properties":{"revision":{"type":"string"}},"required":["revision"],"additionalProperties":false}}`),
	}
}
