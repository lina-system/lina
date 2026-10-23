// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestRefusalReplaysTypedDenialAfterRestart(t *testing.T) {
	for _, kind := range []string{"denied", "no_surface", "stale_revision", "granted_revision", "read_stale_revision", "read_granted_revision", "preset"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			read := kind == "read_stale_revision" || kind == "read_granted_revision"
			marked := !read && kind != "stale_revision" && kind != "granted_revision"
			register := func() {
				if !read {
					f.register(t, marked, nil)
					return
				}
				readSpec := spec(false)
				schema := readSpec.Schema
				readSpec.Schema = func(raw json.RawMessage) (tools.Input, error) {
					in, err := schema(raw)
					in.Write = false
					return in, err
				}
				if err := f.registry.Register(readSpec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
					f.runs.Add(1)
					return tools.Result{State: tools.Applied}, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			register()
			var want error
			switch kind {
			case "denied":
				if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
					t.Fatal(err)
				}
				if err := f.service.Answer(t.Context(), pending(t, f).ID, owner(f), authority.Decision{Choice: authority.Deny}); err != nil {
					t.Fatal(err)
				}
				want = authority.ErrDenied
			case "no_surface":
				f.surfaceAvailable = false
				want = authority.ErrNoSurface
			case "stale_revision", "read_stale_revision":
				f.observedRevision = "external"
				want = grant.ErrStaleRevision
			case "granted_revision", "read_granted_revision":
				f.grantedRevision = "new-grant-revision"
				want = grant.ErrStaleRevision
			case "preset":
				setPreset(t, f, "agent", "investing")
				want = authority.ErrPresetLimit
			}
			if result, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, want) || result.State != tools.Refused {
				t.Fatalf("first refusal: %+v %v", result, err)
			}
			effect, err := f.service.Effect(t.Context(), f.call.Key)
			if err != nil || effect.DenialCode != want.Error() {
				t.Fatalf("denial code not durable: %+v %v", effect, err)
			}
			// Restoring availability/revision must not change the recorded decision.
			f.surfaceAvailable = true
			f.observedRevision = "r1"
			f.grantedRevision = "r1"
			for _, legacy := range []bool{false, true} {
				if legacy {
					// Existing databases have no denial code. Keep their persisted key
					// fingerprint and exercise replay through the same public path.
					if err := f.store.Write(t.Context(), func(tx *canon.Tx) error {
						_, err := tx.ExecContext(t.Context(), "UPDATE authority_effects SET record=json_remove(record, '$.DenialCode') WHERE effect_key=?", f.call.Key)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				for _, restart := range []bool{false, true} {
					if restart {
						if err := f.store.Close(context.Background()); err != nil {
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
						register()
					}
					result, err := f.registry.Dispatch(t.Context(), f.call)
					if !errors.Is(err, want) || result.State != tools.Refused || f.runs.Load() != 0 {
						t.Errorf("replay legacy=%v restart=%v: result=%+v error=%v want=%v runs=%d", legacy, restart, result, err, want, f.runs.Load())
					}
				}
			}
		})
	}
}

func TestRefusalStopsPendingApproval(t *testing.T) {
	for _, kind := range []string{"no_surface", "stale_revision", "approved_stale_revision"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.register(t, true, nil)
			if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
				t.Fatal(err)
			}
			approval := pending(t, f)
			if kind == "approved_stale_revision" {
				if err := f.service.Answer(t.Context(), approval.ID, owner(f), authority.Decision{Choice: authority.Once}); err != nil {
					t.Fatal(err)
				}
			}
			want := authority.ErrNoSurface
			if kind == "no_surface" {
				f.surfaceAvailable = false
			} else {
				f.observedRevision = "external"
				want = grant.ErrStaleRevision
			}
			if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, want) {
				t.Fatal(err)
			}
			rows, err := f.service.Pending(t.Context())
			if err != nil || len(rows) != 0 {
				t.Errorf("refused key still pending: %+v %v", rows, err)
			}
			saved, err := f.service.Approval(t.Context(), approval.ID)
			if err != nil || saved.State != authority.Stopped {
				t.Errorf("approval not stopped: %+v %v", saved, err)
			}
			if err := f.service.Answer(t.Context(), approval.ID, owner(f), authority.Decision{Choice: authority.Once}); !errors.Is(err, authority.ErrStale) {
				t.Errorf("late answer accepted: %v", err)
			}
			if f.runs.Load() != 0 {
				t.Fatal("refused handler ran")
			}
		})
	}
}
