// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

type leaser struct{}

func (leaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

type fixture struct {
	store            *canon.Store
	cfg              canon.Config
	service          *authority.Service
	registry         *tools.Registry
	call             tools.Call
	runs             atomic.Int32
	observedRevision string
	surfaceAvailable bool
}

func setup(t *testing.T) *fixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	f := &fixture{cfg: canon.Config{Path: filepath.Join(t.TempDir(), "canon.sqlite"), Epoch: 7, Controls: controls, Migrations: canon.Migrations()}}
	f.store, err = canon.Open(t.Context(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.Close(context.Background()) })
	f.call = tools.Call{Key: "intent", Name: "edit", Account: "account", Auth: grant.Request{GrantID: "grant", MainID: "main", Epoch: 7, TaskID: "task", Host: "host", OSUser: "user", Person: "person", Agent: "agent", Revision: "r1"}, Args: json.RawMessage(`{"target":"document","body":"hello"}`)}
	f.observedRevision = "r1"
	f.surfaceAvailable = true
	f.open(t)
	return f
}
func (f *fixture) open(t *testing.T) {
	t.Helper()
	var err error
	f.service, err = authority.Open(t.Context(), f.store, authority.Config{Epoch: f.cfg.Epoch, CanPresent: func() bool { return f.surfaceAvailable }, Resolve: func(ctx context.Context, id string) (grant.Grant, grant.Environment, error) {
		r := f.call.Auth
		r.GrantID = id
		r.Epoch = f.cfg.Epoch
		return grant.Grant{Identity: r, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "edit", Target: "document", Write: true}}, Revisions: map[string]string{"document": "r1"}}, grant.Environment{Epoch: f.cfg.Epoch, Now: time.Now(), OSPermitted: true, Revision: f.observedRevision}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.registry = tools.NewRegistry(f.service, leaser{})
}
func spec(marked bool) tools.Spec {
	return tools.Spec{Name: "edit", Plugin: "plugin", Outward: marked, Conc: 2, Deadline: time.Second, Placement: tools.Core,
		Schema: func(raw json.RawMessage) (tools.Input, error) {
			var args struct{ Target, Body string }
			if err := json.Unmarshal(raw, &args); err != nil {
				return tools.Input{}, err
			}
			if args.Target == "" || args.Body == "" {
				return tools.Input{}, tools.ErrInvalid
			}
			return tools.Input{Arguments: raw, Target: args.Target, Write: true, What: "Edit document", Where: args.Target, Undoable: true, ScopeKey: "document-edit"}, nil
		}}
}
func (f *fixture) register(t *testing.T, marked bool, handler tools.Handler) {
	t.Helper()
	if handler == nil {
		handler = func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
			f.runs.Add(1)
			return tools.Result{State: tools.Applied, Body: json.RawMessage(`{"saved":true}`), Revision: "r2"}, nil
		}
	}
	if err := f.registry.Register(spec(marked), handler); err != nil {
		t.Fatal(err)
	}
}

func TestUnmarkedPluginRunsWithoutApprovalAndRecordsEffect(t *testing.T) {
	f := setup(t)
	f.register(t, false, nil)
	result, err := f.registry.Dispatch(t.Context(), f.call)
	if err != nil || result.State != tools.Applied {
		t.Fatalf("dispatch: %+v %v", result, err)
	}
	record, err := f.service.Effect(t.Context(), f.call.Key)
	if err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != 1 || record.Plugin != "plugin" || record.Account != "account" || record.Person != "person" || record.Agent != "agent" || record.ArgumentHash == "" || record.Basis != "grant" || record.State != tools.Applied {
		t.Fatalf("record: %+v", record)
	}
}
func TestIdempotencySurvivesConcurrencyAndRestart(t *testing.T) {
	f := setup(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.register(t, false, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
		f.runs.Add(1)
		once.Do(func() { close(started) })
		<-release
		return tools.Result{State: tools.Applied, Revision: "r2"}, nil
	})
	done := make(chan error, 1)
	go func() { _, err := f.registry.Dispatch(t.Context(), f.call); done <- err }()
	<-started
	_, err := f.registry.Dispatch(t.Context(), f.call)
	if !errors.Is(err, tools.ErrInProgress) {
		t.Fatalf("duplicate: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err = f.store.Close(t.Context()); err != nil {
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
	if err != nil || result.State != tools.Applied || f.runs.Load() != 1 {
		t.Fatalf("replay: %+v %v runs=%d", result, err, f.runs.Load())
	}
	changed := f.call
	changed.Args = json.RawMessage(`{"target":"document","body":"different"}`)
	if _, err = f.registry.Dispatch(t.Context(), changed); !errors.Is(err, tools.ErrKeyConflict) {
		t.Fatalf("key reuse: %v", err)
	}
}
func TestStaleRevisionRefusedBeforeHandler(t *testing.T) {
	f := setup(t)
	f.register(t, false, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); err != nil {
		t.Fatal(err)
	}
	call := f.call
	call.Key = "second-intent"
	if _, err := f.registry.Dispatch(t.Context(), call); !errors.Is(err, grant.ErrStaleRevision) {
		t.Fatalf("stale: %v", err)
	}
	if f.runs.Load() != 1 {
		t.Fatal("stale write ran")
	}
	record, err := f.service.Effect(t.Context(), call.Key)
	if err != nil || record.State != tools.Refused || record.PreviousAgent != "agent" || record.CurrentRevision != "r2" {
		t.Fatalf("refusal: %+v %v", record, err)
	}
}
func TestMarkedPluginRequiresApprovalAndInvalidArgsNeverRun(t *testing.T) {
	f := setup(t)
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	call := f.call
	call.Args = json.RawMessage(`{}`)
	if _, err := f.registry.Dispatch(t.Context(), call); err == nil {
		t.Fatal("invalid arguments accepted")
	}
	if f.runs.Load() != 0 {
		t.Fatal("unapproved call ran")
	}
}
func TestUnknownEffectIsNeverRetried(t *testing.T) {
	f := setup(t)
	f.register(t, false, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
		f.runs.Add(1)
		return tools.Result{}, errors.New("connection lost")
	})
	result, err := f.registry.Dispatch(t.Context(), f.call)
	if err == nil || result.State != tools.Unknown {
		t.Fatalf("result: %+v %v", result, err)
	}
	if _, err = f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrUnknown) {
		t.Fatal(err)
	}
	if f.runs.Load() != 1 {
		t.Fatal("unknown effect retried")
	}
}

func TestReplayReturnsReceiptAfterObservedRevisionChanges(t *testing.T) {
	f := setup(t)
	f.register(t, false, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); err != nil {
		t.Fatal(err)
	}
	f.observedRevision = "r2"
	result, err := f.registry.Dispatch(t.Context(), f.call)
	if err != nil || result.State != tools.Applied || f.runs.Load() != 1 {
		t.Fatalf("receipt replay: %+v %v", result, err)
	}
}
