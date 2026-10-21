// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestSandboxedNetworkApprovalControlsActualConnection(t *testing.T) {
	lina := buildLina(t)
	canary := filepath.Join(t.TempDir(), "network-canary")
	build := osexec.Command("go", "build", "-o", canary, "../../../kit/tools/testdata/network")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build canary: %v %s", err, out)
	}
	for _, choice := range []authority.Choice{authority.Once, authority.Always, authority.Deny} {
		t.Run(string(choice), func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				for {
					c, err := listener.Accept()
					if err != nil {
						return
					}
					c.Close()
				}
			}()
			defer func() { listener.Close(); <-joined }()
			root, versions, state := t.TempDir(), t.TempDir(), t.TempDir()
			for _, dir := range []string{versions, state} {
				if err := os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			owner := proc.NewOwner()
			defer owner.Close(t.Context())
			e, err := coreexec.New(owner, coreexec.Config{Scope: "workspace", Root: root, Versions: versions, Lina: lina, ReadDenied: []string{state}, Env: []string{"PATH=/usr/bin:/bin"}})
			if err != nil {
				t.Fatal(err)
			}
			l := lease.New(func() {})
			defer l.Release()
			initial, err := e.Snapshot(t.Context(), l)
			if err != nil {
				t.Fatal(err)
			}
			controls, err := ctlfiles.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer controls.Close()
			if err := controls.Acquire(); err != nil {
				t.Fatal(err)
			}
			store, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(state, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close(context.Background())
			identity := grant.Request{GrantID: "grant", MainID: "main", Epoch: 1, TaskID: "task", Host: "host", OSUser: "user", Person: "person", Agent: "agent", Revision: initial.Revision}
			service, err := authority.Open(t.Context(), store, authority.Config{Epoch: 1, CanPresent: func() bool { return true }, Resolve: func(ctx context.Context, _ string) (grant.Grant, grant.Environment, error) {
				observed, err := e.Snapshot(ctx, l)
				return grant.Grant{Identity: identity, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "exec_command", Target: "workspace", Write: true}}, Revisions: map[string]string{"workspace": identity.Revision}}, grant.Environment{Epoch: 1, Now: time.Now(), OSPermitted: true, Revision: observed.Revision}, err
			}})
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry(service, leaser{})
			argv := []string{canary, listener.Addr().String(), filepath.Join(root, "receipt")}
			if _, err := e.RegisterCommand(registry, tools.CommandPolicy{Name: "exec_command", Target: "workspace", Prefix: argv}); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(tools.CommandRequest{Argv: argv})
			call := tools.Call{Key: "blocked-network", Name: "exec_command", Auth: identity, Args: args}
			result, err := registry.Dispatch(t.Context(), call)
			if err == nil || result.State != tools.Failed {
				t.Fatalf("network was not blocked: %+v %v", result, err)
			}
			args, _ = json.Marshal(tools.CommandRequest{Argv: argv, Network: true})
			call.Args = args
			call.Key = "ask"
			if _, err := registry.Dispatch(t.Context(), call); !errors.Is(err, tools.ErrApprovalRequired) {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "receipt")); !os.IsNotExist(err) {
				t.Fatal("command ran before answer")
			}
			pending, err := service.Pending(t.Context())
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending: %+v %v", pending, err)
			}
			if err := service.Answer(t.Context(), pending[0].ID, authority.Decider{Person: "person", Epoch: 1, Surface: authority.App, Authenticated: true}, authority.Decision{Choice: choice}); err != nil {
				t.Fatal(err)
			}
			result, err = registry.Dispatch(t.Context(), call)
			if choice == authority.Deny {
				if !errors.Is(err, authority.ErrDenied) {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, "receipt")); !os.IsNotExist(err) {
					t.Fatal("denied command ran")
				}
			} else {
				if err != nil || result.State != tools.Applied {
					t.Fatalf("approved: %+v %v", result, err)
				}
				var receipt coreexec.CommandResult
				if err := json.Unmarshal(result.Body, &receipt); err != nil || receipt.Stdout != "connected" || len(receipt.Changes) != 1 || receipt.Changes[0].Path != "receipt" {
					t.Fatalf("command receipt: %+v %v", receipt, err)
				}
				identity.Revision = result.Revision
				call.Auth = identity
			}
			call.Key = "next"
			result, err = registry.Dispatch(t.Context(), call)
			if choice == authority.Always {
				if err != nil || result.State != tools.Applied {
					t.Fatalf("standing: %+v %v", result, err)
				}
			} else if !errors.Is(err, tools.ErrApprovalRequired) {
				t.Fatalf("once/deny repeated: %v", err)
			}
		})
	}
}
