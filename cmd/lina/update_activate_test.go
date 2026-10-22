// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/recover"
)

func TestResumeUpdateFailureHoldsInstall(t *testing.T) {
	for _, phase := range []string{"complete", "rolled_back", "snapshot"} {
		for _, failure := range []string{"start", "readiness"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				s, err := ctlfiles.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if err := s.AcquireUpdate(); err != nil {
					t.Fatal(err)
				}
				r := ctlfiles.UpdateRecord{
					Schema: 1, ID: "attempt", Root: root,
					Socket: filepath.Join(root, "missing.sock"), Phase: phase,
					Candidate:    filepath.Join(root, "system/updates/candidate"),
					Previous:     filepath.Join(root, "system/updates/previous-attempt"),
					ReachedReady: phase == "complete",
				}
				if err := s.WriteUpdate(r); err != nil {
					t.Fatal(err)
				}
				injected := errors.New("service start failed")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var calls []string
				service := func(callCtx context.Context, verb string) error {
					calls = append(calls, verb)
					if verb == "stop" && callCtx.Err() != nil {
						t.Fatal("cleanup inherited cancelled readiness context")
					}
					if failure == "start" {
						return injected
					}
					// Cancellation before the readiness probe is deterministic and
					// must still stop the service and persist the failure.
					cancel()
					return nil
				}
				got, err := resumeUpdate(ctx, s, r, service)
				if err == nil || (failure == "start" && !errors.Is(err, injected)) {
					t.Fatalf("lost resume failure: %v", err)
				}
				if got.Phase != "held" || got.Cause != "update_resume_failed" || got.ReachedReady != r.ReachedReady {
					t.Fatalf("failed resume did not hold installation: phase=%s cause=%q ready=%v", got.Phase, got.Cause, got.ReachedReady)
				}
				persisted, err := s.ReadUpdate()
				if err != nil || persisted == nil || !reflect.DeepEqual(*persisted, got) {
					t.Fatalf("hold not durable: %+v %v", persisted, err)
				}
				want := []string{"start"}
				if failure == "readiness" {
					want = append(want, "stop")
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("service calls = %v, want %v", calls, want)
				}
				if err := recover.UpdateStart(s, root, r.Socket); !errors.Is(err, recover.ErrUpdateHeld) {
					t.Fatalf("failed resume admitted normal Core: %v", err)
				}
			})
		}
	}
}
