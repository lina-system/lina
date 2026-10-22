// SPDX-License-Identifier: Apache-2.0
package switcher

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestProcedureRefusesWithoutApprovalAndRollsBackFailure(t *testing.T) {
	for _, failure := range []string{"unapproved", "running", "disable", "activation", "success"} {
		t.Run(failure, func(t *testing.T) {
			var calls []string
			state := "approved"
			if failure == "unapproved" {
				state = ""
			}
			manifest := Manifest{SwitchID: "switch", Tasks: []string{"task"}, Coverage: "all parent sessions and flow engines stopped", StateDirectory: "/fixture/state", Executable: "/fixture/relay"}
			digest := manifest.Digest()
			external := Observation{Enabled: boolp(true), Running: boolp(true), Lock: "held", StateDirectory: manifest.StateDirectory, ScopeAuthority: "production", Conflicts: []any{}}
			core := func(_ context.Context, p protocol.WorkSwitchRequest) (protocol.WorkSwitchResult, error) {
				calls = append(calls, p.Operation)
				switch p.Operation {
				case "activate_switch":
					if failure == "activation" {
						return protocol.WorkSwitchResult{}, errors.New("lost activation")
					}
					state = "active"
				case "rollback_switch":
					state = "rollback_pending"
				case "release_switch":
					state = "rolled_back"
				}
				return protocol.WorkSwitchResult{SwitchID: "switch", State: state, Approval: "owner-input", Snapshot: digest, Tasks: manifest.Tasks}, nil
			}
			command := func(_ context.Context, action string) ([]byte, error) {
				calls = append(calls, action)
				switch action {
				case "disable":
					external.Enabled = boolp(false)
					if failure == "disable" {
						return nil, errors.New("stop failed")
					}
					if failure != "running" {
						external.Running = boolp(false)
						external.Lock = "free"
					}
				case "enable":
					external.Enabled = boolp(true)
				case "start":
					external.Running = boolp(true)
					external.Lock = "held"
				}
				return encodeObservation(external), nil
			}
			p := Procedure{Manifest: manifest, Core: core, Command: command, Journal: filepath.Join(t.TempDir(), "journal.json")}
			err := p.Apply(t.Context())
			if failure == "success" {
				if err != nil || state != "active" {
					t.Fatal(state, err)
				}
				return
			}
			if err == nil {
				t.Fatal("failure reported as success")
			}
			if failure == "unapproved" {
				if !reflect.DeepEqual(calls, []string{"switch_status"}) {
					t.Fatalf("effects without approval: %v", calls)
				}
				return
			}
			if state != "rolled_back" {
				t.Fatalf("no explicit rollback: %s %v", state, calls)
			}
			release, restore := -1, -1
			for i, c := range calls {
				if c == "release_switch" {
					release = i
				}
				if c == "enable" {
					restore = i
				}
			}
			if release < 0 || restore <= release {
				t.Fatalf("restored before LINA drained: %v", calls)
			}
			if !*external.Enabled || !*external.Running {
				t.Fatal("original service state not restored")
			}
		})
	}
}
func boolp(v bool) *bool { return &v }

func TestUnknownObservationAndJournalCannotAuthorizeTransfer(t *testing.T) {
	manifest := Manifest{StateDirectory: "/scope"}
	valid := Observation{Enabled: boolp(false), Running: boolp(false), Lock: "free", StateDirectory: "/scope", ScopeAuthority: "production", Conflicts: []any{}}
	for _, mutate := range []func(*Observation){
		func(o *Observation) { o.Enabled = nil }, func(o *Observation) { o.Running = nil }, func(o *Observation) { o.Lock = "held" }, func(o *Observation) { o.StateDirectory = "/other" }, func(o *Observation) { o.ScopeAuthority = "isolated" }, func(o *Observation) { o.Conflicts = nil }, func(o *Observation) { o.Conflicts = []any{"other scope"} },
	} {
		o := valid
		mutate(&o)
		if o.valid(manifest) {
			t.Fatal("unknown or conflicting scope accepted", o)
		}
	}
}
func TestOperatorLockAndUnknownExecutionHoldRecovery(t *testing.T) {
	manifest := Manifest{SwitchID: "switch", Tasks: []string{"task"}, StateDirectory: "/scope", Coverage: "fixture inventory"}
	p := Procedure{Manifest: manifest, Journal: filepath.Join(t.TempDir(), "journal.json")}
	lock, err := p.lock()
	if err != nil {
		t.Fatal(err)
	}
	if second, err := p.lock(); err == nil {
		second.Close()
		t.Fatal("two operators acquired journal")
	}
	lock.Close()
	original := Observation{Enabled: boolp(true), Running: boolp(true), Lock: "held", StateDirectory: "/scope", ScopeAuthority: "production", Conflicts: []any{}}
	if err = p.save(Journal{Digest: manifest.Digest(), Before: &original}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.Command = func(context.Context, string) ([]byte, error) { calls++; return nil, nil }
	p.Core = func(_ context.Context, q protocol.WorkSwitchRequest) (protocol.WorkSwitchResult, error) {
		if q.Operation == "release_switch" {
			return protocol.WorkSwitchResult{}, errors.New("execution unknown")
		}
		return protocol.WorkSwitchResult{State: "rollback_pending", Snapshot: manifest.Digest()}, nil
	}
	if err = p.Rollback(t.Context()); err == nil {
		t.Fatal("unknown execution released")
	}
	if calls != 0 {
		t.Fatal("external restoration before LINA release")
	}
}
