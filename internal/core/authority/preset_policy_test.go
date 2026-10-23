// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestRestrictedPresetRefusesUnclassifiedBeforeApprovalOrDispatch(t *testing.T) {
	t.Parallel()
	for _, preset := range []string{"investing", "legal"} {
		for _, outward := range []bool{false, true} {
			t.Run(preset+map[bool]string{false: "/local", true: "/outward"}[outward], func(t *testing.T) {
				t.Parallel()
				f := setup(t)
				err := f.store.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), "INSERT INTO agents(id,name,preset,revision,state) VALUES(?,?,?,?,?)", "agent", "Renamed persona", preset, 1, []byte(`{}`))
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				f.register(t, outward, nil)
				result, err := f.registry.Dispatch(t.Context(), f.call)
				if err == nil || result.State != tools.Refused || f.runs.Load() != 0 {
					t.Fatalf("restricted preset dispatched: result=%+v err=%v calls=%d", result, err, f.runs.Load())
				}
				var pending int
				if err := f.store.Read(t.Context(), func(tx *canon.Tx) error {
					return tx.QueryRowContext(context.Background(), "SELECT count(*) FROM authority_approvals").Scan(&pending)
				}); err != nil {
					t.Fatal(err)
				}
				if pending != 0 {
					t.Fatalf("preset refusal raised %d approvals", pending)
				}
				effect, err := f.service.Effect(t.Context(), f.call.Key)
				if err != nil || effect.State != tools.Refused || effect.Person != "person" || effect.Agent != "agent" || effect.Target != "document" {
					t.Fatalf("missing refusal provenance: %+v %v", effect, err)
				}
			})
		}
	}
}

func setPreset(t *testing.T, f *fixture, agent, preset string) {
	t.Helper()
	if err := f.store.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO agents(id,name,preset,revision,state) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name", agent, "Unrelated display name", preset, 1, []byte(`{}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func classifiedTool(t *testing.T, f *fixture, class tools.EffectClass, placement tools.Placement, outward bool) {
	t.Helper()
	s := spec(outward)
	s.Placement = placement
	s.Classify = tools.Class(class)
	if class == tools.Research || class == tools.Calculation {
		schema := s.Schema
		s.Schema = func(raw json.RawMessage) (tools.Input, error) {
			in, err := schema(raw)
			in.Write = false
			return in, err
		}
	}
	if err := f.registry.Register(s, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
		f.runs.Add(1)
		return tools.Result{State: tools.Applied, Revision: "r2"}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPresetEffectMatrixAndRefusalEvidence(t *testing.T) {
	t.Parallel()
	for _, preset := range []string{"investing", "legal", "system", "dev"} {
		for _, class := range []tools.EffectClass{tools.Research, tools.Calculation, tools.PrivateDraft, tools.LocalWrite, tools.InvestmentOrder, tools.BindingExecution, tools.ExternalEffect, tools.CommandEffect, tools.WorkerEffect, tools.Unclassified} {
			for _, placement := range []tools.Placement{tools.Core, tools.Node} {
				t.Run(preset+"/"+string(class)+"/"+string(placement), func(t *testing.T) {
					t.Parallel()
					f := setup(t)
					setPreset(t, f, "agent", preset)
					classifiedTool(t, f, class, placement, false)
					result, err := f.registry.Dispatch(t.Context(), f.call)
					restricted := preset == "investing" || preset == "legal"
					permitted := class == tools.Research || class == tools.Calculation || class == tools.PrivateDraft || class == tools.LocalWrite
					outward := class == tools.InvestmentOrder || class == tools.BindingExecution || class == tools.ExternalEffect
					if !restricted && outward {
						if !errors.Is(err, tools.ErrApprovalRequired) || f.runs.Load() != 0 {
							t.Fatalf("ordinary outward effect skipped approval: %+v %v", result, err)
						}
						pending, e := f.service.Pending(t.Context())
						if e != nil || len(pending) != 1 {
							t.Fatalf("approval missing: %+v %v", pending, e)
						}
						if e = f.service.Answer(t.Context(), pending[0].ID, authority.Decider{Person: "person", Epoch: 7, Surface: authority.App, Authenticated: true}, authority.Decision{Choice: authority.Once}); e != nil {
							t.Fatal(e)
						}
						result, err = f.registry.Dispatch(t.Context(), f.call)
					}
					if restricted && !permitted {
						if !errors.Is(err, authority.ErrPresetLimit) || result.State != tools.Refused || f.runs.Load() != 0 {
							t.Fatalf("limit bypassed: %+v %v", result, err)
						}
						var reason struct {
							Code, Boundary string
							Class          tools.EffectClass `json:"effect_class"`
						}
						if json.Unmarshal(result.Body, &reason) != nil || reason.Code != "preset_effect_limit" || reason.Boundary == "" || reason.Class != class {
							t.Fatalf("missing boundary: %s", result.Body)
						}
					} else if err != nil || result.State != tools.Applied {
						t.Fatalf("permitted effect failed: %+v %v", result, err)
					}
					effect, err := f.service.Effect(t.Context(), f.call.Key)
					if err != nil || effect.Class != class || effect.Person != "person" || effect.Agent != "agent" || effect.Target != "document" {
						t.Fatalf("effect evidence: %+v %v", effect, err)
					}
				})
			}
		}
	}
}

func TestDelegationIntersectsOriginAndReceiverAndSurvivesRestart(t *testing.T) {
	t.Parallel()
	f := setup(t)
	setPreset(t, f, "origin", "investing")
	setPreset(t, f, "legal-agent", "legal")
	setPreset(t, f, "agent", "dev")
	if err := f.service.BindDelegation(t.Context(), "person", "origin", "origin-turn", "legal-agent", "relay-turn"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.BindDelegation(t.Context(), "person", "legal-agent", "relay-turn", "agent", "task"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.BindDelegation(t.Context(), "person", "agent", "clean-turn", "agent", "task"); !errors.Is(err, tools.ErrKeyConflict) {
		t.Fatalf("rebind widened limits: %v", err)
	}
	// Source deletion cannot erase the inherited restriction.
	if err := f.store.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM agents WHERE id IN ('origin','legal-agent')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.cfg.Epoch++
	f.call.Auth.Epoch++
	var err error
	f.store, err = canon.Open(t.Context(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.open(t)
	classifiedTool(t, f, tools.WorkerEffect, tools.Node, false)
	result, err := f.registry.Dispatch(t.Context(), f.call)
	if !errors.Is(err, authority.ErrPresetLimit) || result.State != tools.Refused || f.runs.Load() != 0 {
		t.Fatalf("relay lost restriction: %+v %v", result, err)
	}
	var body struct {
		Limits authority.Limits `json:"limits"`
	}
	if json.Unmarshal(result.Body, &body) != nil || !body.Limits.AdviceOnly || !body.Limits.DraftOnly {
		t.Fatalf("not an intersection: %s", result.Body)
	}
}

func TestRestrictedWorkerApprovalNeverRaisesAnApproval(t *testing.T) {
	t.Parallel()
	f := setup(t)
	setPreset(t, f, "agent", "legal")
	request := workerApproval()
	if _, err := f.service.RequestWorkerApproval(t.Context(), request); !errors.Is(err, authority.ErrPresetLimit) {
		t.Fatalf("worker approval bypassed ceiling: %v", err)
	}
	effect, err := f.service.Effect(t.Context(), "preset:"+request.EffectKey)
	if err != nil || effect.Class != tools.WorkerEffect || effect.State != tools.Refused || effect.Person != request.Person || effect.Agent != request.Agent {
		t.Fatalf("missing worker refusal: %+v %v", effect, err)
	}
}

func TestAcceptedMergeCannotBypassRestrictedOrigin(t *testing.T) {
	t.Parallel()
	f := setup(t)
	setPreset(t, f, "agent", "legal")
	merges := authority.NewMerges(f.store, 7)
	b := authority.MergeBinding{ID: "task", Person: "person", Agent: "agent", Repository: "example/project", Number: 3, Base: "dev", Clone: "/synthetic", Protected: map[string]string{"refs/heads/dev": "base"}}
	if err := merges.Bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := merges.Accept(t.Context(), b.ID, b.Person, "head"); err != nil {
		t.Fatal(err)
	}
	if err := merges.Begin(t.Context(), b.ID, b.Person, "head", b.Protected); !errors.Is(err, authority.ErrPresetLimit) {
		t.Fatalf("accepted delivery executed externally: %v", err)
	}
	got, err := merges.Get(t.Context(), b.ID, b.Person)
	if err != nil || got.State != "ready" {
		t.Fatalf("refusal reserved external effect: %+v %v", got, err)
	}
}

func TestApprovalAndStandingGrantCannotLiftPresetCeiling(t *testing.T) {
	t.Parallel()
	t.Run("accepted approval", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		f.register(t, true, nil)
		if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
			t.Fatal(err)
		}
		pending, err := f.service.Pending(t.Context())
		if err != nil || len(pending) != 1 {
			t.Fatal(pending, err)
		}
		if err = f.service.Answer(t.Context(), pending[0].ID, authority.Decider{Person: "person", Epoch: 7, Surface: authority.App, Authenticated: true}, authority.Decision{Choice: authority.Once}); err != nil {
			t.Fatal(err)
		}
		setPreset(t, f, "agent", "investing")
		if _, err = f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, authority.ErrPresetLimit) || f.runs.Load() != 0 {
			t.Fatalf("accepted button lifted ceiling: %v", err)
		}
	})
	t.Run("standing network grant", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		setPreset(t, f, "agent", "legal")
		s := spec(false)
		s.Plugin = ""
		schema := s.Schema
		s.Schema = func(raw json.RawMessage) (tools.Input, error) {
			in, err := schema(raw)
			in.Network = true
			in.Write = false
			return in, err
		}
		s.Classify = func(in tools.Input) tools.EffectClass {
			var a struct{ Body string }
			_ = json.Unmarshal(in.Arguments, &a)
			if a.Body == "research" {
				return tools.Research
			}
			return tools.CommandEffect
		}
		if err := f.registry.Register(s, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
			f.runs.Add(1)
			return tools.Result{State: tools.Applied}, nil
		}); err != nil {
			t.Fatal(err)
		}
		f.call.Args = json.RawMessage(`{"target":"document","body":"research"}`)
		if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
			t.Fatal(err)
		}
		pending, err := f.service.Pending(t.Context())
		if err != nil || len(pending) != 1 {
			t.Fatal(pending, err)
		}
		if err = f.service.Answer(t.Context(), pending[0].ID, authority.Decider{Person: "person", Epoch: 7, Surface: authority.App, Authenticated: true}, authority.Decision{Choice: authority.Always}); err != nil {
			t.Fatal(err)
		}
		if _, err = f.registry.Dispatch(t.Context(), f.call); err != nil {
			t.Fatal(err)
		}
		f.call.Key = "execute-draft"
		f.call.Args = json.RawMessage(`{"target":"document","body":"execute accepted draft"}`)
		if _, err = f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, authority.ErrPresetLimit) || f.runs.Load() != 1 {
			t.Fatalf("standing grant lifted ceiling: %v calls=%d", err, f.runs.Load())
		}
	})
}
