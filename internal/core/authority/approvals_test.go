// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

func owner(f *fixture) authority.Decider {
	return authority.Decider{Person: "person", Epoch: f.cfg.Epoch, Surface: authority.App, Authenticated: true}
}
func pending(t *testing.T, f *fixture) authority.Approval {
	t.Helper()
	rows, err := f.service.Pending(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending: %+v %v", rows, err)
	}
	return rows[0]
}
func TestApprovalPresentationIsComputedFromValidatedArguments(t *testing.T) {
	f := setup(t)
	f.register(t, true, nil)
	f.call.Args = json.RawMessage(`{"target":"document","body":"hello","what":"pretend read","where":"elsewhere","undoable":false}`)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	if a.What != "Edit document" || a.Where != "document" || !a.Undoable || a.Person != "person" || a.Agent != "agent" || a.Epoch != 7 || a.ScopeKey != "document-edit" || a.State != authority.Pending || !reflect.DeepEqual(a.Choices, []authority.Choice{authority.Once, authority.Deny}) || !json.Valid(a.Arguments) {
		t.Fatalf("approval: %+v", a)
	}
	// Repeated admission presents the same opaque request, without creating one
	// per transport attempt or trusting any presentation fields in the arguments.
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	if b := pending(t, f); b.ID != a.ID {
		t.Fatal("duplicated approval")
	}
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.registry.Dispatch(t.Context(), f.call); err != nil {
		t.Fatal(err)
	}
	e, err := f.service.Effect(t.Context(), f.call.Key)
	if err != nil || e.Basis != "approval:"+a.ID {
		t.Fatalf("basis: %+v %v", e, err)
	}
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); !errors.Is(err, authority.ErrStale) {
		t.Fatalf("duplicate answer: %v", err)
	}
}
func (f *fixture) registerNetwork(t *testing.T) {
	t.Helper()
	s := spec(false)
	s.Plugin = ""
	validate := s.Schema
	s.Schema = func(raw json.RawMessage) (tools.Input, error) {
		in, err := validate(raw)
		in.Write = false
		in.Network = true
		in.What = "Allow network"
		in.ScopeKey = "network:service"
		return in, err
	}
	if err := f.registry.Register(s, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
		f.runs.Add(1)
		return tools.Result{State: tools.Applied}, nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestNetworkStandingGrantRevokesInOneAction(t *testing.T) {
	f := setup(t)
	f.registerNetwork(t)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	if !reflect.DeepEqual(a.Choices, []authority.Choice{authority.Once, authority.Always, authority.Deny}) {
		t.Fatal(a.Choices)
	}
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Always}); err != nil {
		t.Fatal(err)
	}
	// Always means every agent unless the owner explicitly restricted it.
	if _, err := f.registry.Dispatch(t.Context(), f.call); err != nil {
		t.Fatal(err)
	}
	f.call.Key = "second-agent-intent"
	f.call.Auth.Agent = "second-agent"
	if _, err := f.registry.Dispatch(t.Context(), f.call); err != nil {
		t.Fatal(err)
	}
	grants, err := f.service.Standing(t.Context(), "person")
	if err != nil || len(grants) != 1 || !grants[0].AllAgents {
		t.Fatalf("standing: %+v %v", grants, err)
	}
	if err := f.service.RevokeStanding(t.Context(), grants[0].ID, owner(f)); err != nil {
		t.Fatal(err)
	}
	f.call.Key = "after-revoke"
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatalf("revoked: %v", err)
	}
	if f.runs.Load() != 2 {
		t.Fatal("revoked network call ran")
	}
	grants, err = f.service.Standing(t.Context(), "person")
	if err != nil || !grants[0].Revoked || grants[0].RevokedBy != "person" {
		t.Fatalf("revocation audit: %+v %v", grants, err)
	}
}
func TestPendingApprovalStopsAtRestartAndLateAnswerIsStale(t *testing.T) {
	f := setup(t)
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
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
	f.register(t, true, nil)
	stopped, err := f.service.Approval(t.Context(), a.ID)
	if err != nil || stopped.State != authority.Stopped {
		t.Fatalf("restart: %+v %v", stopped, err)
	}
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); !errors.Is(err, authority.ErrStale) {
		t.Fatalf("late: %v", err)
	}
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, authority.ErrStale) {
		t.Fatalf("old intent resumed: %v", err)
	}
	if f.runs.Load() != 0 {
		t.Fatal("stopped approval ran")
	}
}
func TestApprovalRejectsTextWrongOwnerAndPluginStandingGrant(t *testing.T) {
	for _, name := range []string{"text", "wrong-owner", "unauthenticated", "descendant", "old-epoch", "plugin-always"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.register(t, true, nil)
			if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
				t.Fatal(err)
			}
			a := pending(t, f)
			d := owner(f)
			choice := authority.Once
			switch name {
			case "text":
				d.Surface = authority.Text
			case "wrong-owner":
				d.Person = "other"
			case "unauthenticated":
				d.Authenticated = false
			case "descendant":
				d.CoreDescendant = true
			case "old-epoch":
				d.Epoch--
			case "plugin-always":
				choice = authority.Always
			}
			if err := f.service.Answer(t.Context(), a.ID, d, authority.Decision{Choice: choice}); err == nil {
				t.Fatal("invalid answer accepted")
			}
			if got := pending(t, f); got.ID != a.ID {
				t.Fatal("request changed")
			}
		})
	}
}
func TestNetworkGrantAgentRestrictionAndChannelChoices(t *testing.T) {
	f := setup(t)
	f.registerNetwork(t)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	d := owner(f)
	d.Surface = authority.Channel
	d.ChatID = "chat"
	d.MessageID = "message"
	if err := f.service.Answer(t.Context(), a.ID, d, authority.Decision{Choice: authority.Always}); err == nil {
		t.Fatal("channel created standing grant")
	}
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Always, Agents: &authority.AgentScope{IDs: []string{"agent"}}}); err != nil {
		t.Fatal(err)
	}
	f.call.Key = "other-agent"
	f.call.Auth.Agent = "other"
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatalf("restricted grant widened: %v", err)
	}
}

func TestApprovalCannotAuthorizeChangedPresentation(t *testing.T) {
	f := setup(t)
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); err != nil {
		t.Fatal(err)
	}
	changed := tools.NewRegistry(f.service, leaser{})
	s := spec(true)
	validate := s.Schema
	s.Schema = func(raw json.RawMessage) (tools.Input, error) {
		in, err := validate(raw)
		in.Undoable = false
		in.What = "Permanently overwrite"
		return in, err
	}
	if err := changed.Register(s, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
		t.Error("changed plan ran")
		return tools.Result{State: tools.Applied, Revision: "r2"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := changed.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrKeyConflict) {
		t.Fatalf("changed plan: %v", err)
	}
}
func TestStandingGrantPersistsButRevokedGrantCannotAuthorizeApprovedRequest(t *testing.T) {
	f := setup(t)
	f.registerNetwork(t)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Always}); err != nil {
		t.Fatal(err)
	}
	grants, err := f.service.Standing(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.RevokeStanding(t.Context(), grants[0].ID, owner(f)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, authority.ErrStale) {
		t.Fatalf("revoked approval resumed: %v", err)
	}
	// Establish a fresh grant, then restart before using it.
	f.call.Key = "fresh"
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a = pending(t, f)
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Always}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.cfg.Epoch++
	f.call.Auth.Epoch++
	f.store, err = canon.Open(t.Context(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.open(t)
	f.registerNetwork(t)
	f.call.Key = "after-restart"
	if _, err := f.registry.Dispatch(t.Context(), f.call); err != nil {
		t.Fatalf("standing lost at restart: %v", err)
	}
}
func TestEmptyNamedAgentScopeDoesNotBecomeAllAgents(t *testing.T) {
	f := setup(t)
	f.registerNetwork(t)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Always, Agents: &authority.AgentScope{}}); err != nil {
		t.Fatal(err)
	}
	f.call.Key = "new-intent"
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatalf("empty scope widened: %v", err)
	}
}
func TestDeniedApprovalRecordsRefusal(t *testing.T) {
	f := setup(t)
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	d := owner(f)
	d.Surface = authority.Channel
	d.ChatID = "chat"
	d.MessageID = "message"
	if err := f.service.Answer(t.Context(), a.ID, d, authority.Decision{Choice: authority.Deny}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, authority.ErrDenied) {
		t.Fatal(err)
	}
	a, err := f.service.Approval(t.Context(), a.ID)
	if err != nil || a.AnsweredBy != "person" || a.ChatID != "chat" || a.MessageID != "message" {
		t.Fatalf("answer audit: %+v %v", a, err)
	}
	e, err := f.service.Effect(t.Context(), f.call.Key)
	if err != nil || e.State != tools.Refused || f.runs.Load() != 0 {
		t.Fatalf("denied: %+v %v", e, err)
	}
}

func TestNoApprovalSurfaceRefusesAndRecordsEffect(t *testing.T) {
	f := setup(t)
	f.surfaceAvailable = false
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, authority.ErrNoSurface) {
		t.Fatalf("no surface: %v", err)
	}
	rows, err := f.service.Pending(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("inaccessible request: %+v %v", rows, err)
	}
	e, err := f.service.Effect(t.Context(), f.call.Key)
	if err != nil || e.State != tools.Refused || f.runs.Load() != 0 {
		t.Fatalf("refusal: %+v %v", e, err)
	}
}

func TestOnlyOneConcurrentApprovalAnswerIsAccepted(t *testing.T) {
	f := setup(t)
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	a := pending(t, f)
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			results <- f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once})
		}()
	}
	close(start)
	successes := 0
	stale := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, authority.ErrStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("accepted=%d stale=%d", successes, stale)
	}
}
func TestInterruptedClaimBecomesUnknownAndRetainsTarget(t *testing.T) {
	f := setup(t)
	s := spec(false)
	in, err := s.Schema(f.call.Args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Begin(t.Context(), f.call, s, in); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.cfg.Epoch++
	f.call.Auth.Epoch++
	f.store, err = canon.Open(t.Context(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.open(t)
	f.register(t, false, nil)
	result, err := f.registry.Dispatch(t.Context(), f.call)
	if !errors.Is(err, tools.ErrUnknown) || result.State != tools.Unknown {
		t.Fatalf("interrupted: %+v %v", result, err)
	}
	f.call.Key = "new-intent"
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrInProgress) {
		t.Fatalf("unresolved target overwritten: %v", err)
	}
	if f.runs.Load() != 0 {
		t.Fatal("interrupted effect re-executed")
	}
}
