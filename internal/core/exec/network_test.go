// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestNetworkWaitsForOnceAlwaysOrDeny(t *testing.T) {
	for _, choice := range []authority.Choice{authority.Once, authority.Always, authority.Deny} {
		t.Run(string(choice), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			controls, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer controls.Close()
			if err := controls.Acquire(); err != nil {
				t.Fatal(err)
			}
			store, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Controls: controls, Epoch: 1, Migrations: canon.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close(context.Background())
			identity := grant.Request{GrantID: "grant", MainID: "main", Epoch: 1, TaskID: "task", Host: "host", OSUser: "user", Person: "person", Agent: "agent"}
			service, err := authority.Open(t.Context(), store, authority.Config{Epoch: 1, CanPresent: func() bool { return true }, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
				return grant.Grant{Identity: identity, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "exec_command", Target: "workspace"}}}, grant.Environment{Epoch: 1, Now: time.Now(), OSPermitted: true}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry(service, leaser{})
			spec, err := tools.CommandSpec(tools.CommandPolicy{Name: "exec_command", Target: "workspace", Prefix: []string{"fetch"}})
			if err != nil {
				t.Fatal(err)
			}
			runs := 0
			if err := registry.Register(spec, func(_ context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
				var command tools.CommandRequest
				if err := json.Unmarshal(in.Arguments, &command); err != nil {
					t.Fatal(err)
				}
				if !command.Network {
					t.Fatal("lost network request")
				}
				runs++
				return tools.Result{State: tools.Applied}, nil
			}); err != nil {
				t.Fatal(err)
			}
			call := tools.Call{Key: "first", Name: spec.Name, Auth: identity, Args: json.RawMessage(`{"argv":["fetch","document"],"network":true}`)}
			result, err := registry.Dispatch(t.Context(), call)
			if !errors.Is(err, tools.ErrApprovalRequired) || result.State != tools.Refused || runs != 0 {
				t.Fatalf("did not park: %+v %v runs=%d", result, err, runs)
			}
			pending, err := service.Pending(t.Context())
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending: %+v %v", pending, err)
			}
			if !reflect.DeepEqual(pending[0].Choices, []authority.Choice{authority.Once, authority.Always, authority.Deny}) {
				t.Fatal(pending[0].Choices)
			}
			if err := service.Answer(t.Context(), pending[0].ID, authority.Decider{Person: "person", Epoch: 1, Surface: authority.App, Authenticated: true}, authority.Decision{Choice: choice}); err != nil {
				t.Fatal(err)
			}
			result, err = registry.Dispatch(t.Context(), call)
			if choice == authority.Deny {
				if !errors.Is(err, authority.ErrDenied) || runs != 0 {
					t.Fatalf("denial ran: %v runs=%d", err, runs)
				}
			} else if err != nil || result.State != tools.Applied || runs != 1 {
				t.Fatalf("approved dispatch: %+v %v runs=%d", result, err, runs)
			}
			call.Key = "next"
			call.Args = json.RawMessage(`{"argv":["fetch","another"],"network":true}`)
			_, err = registry.Dispatch(t.Context(), call)
			if choice == authority.Always {
				if err != nil || runs != 2 {
					t.Fatalf("standing grant: %v runs=%d", err, runs)
				}
				grants, err := service.Standing(t.Context(), "person")
				if err != nil || len(grants) != 1 {
					t.Fatal(err)
				}
				if err := service.RevokeStanding(t.Context(), grants[0].ID, authority.Decider{Person: "person", Epoch: 1, Surface: authority.App, Authenticated: true}); err != nil {
					t.Fatal(err)
				}
				call.Key = "revoked"
				if _, err = registry.Dispatch(t.Context(), call); !errors.Is(err, tools.ErrApprovalRequired) || runs != 2 {
					t.Fatalf("revocation: %v runs=%d", err, runs)
				}
			} else if !errors.Is(err, tools.ErrApprovalRequired) {
				t.Fatalf("once/deny became standing: %v", err)
			}
		})
	}
}
