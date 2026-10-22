// SPDX-License-Identifier: Apache-2.0
package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestActivationFailureBoundaries(t *testing.T) {
	for _, failure := range []string{"drain", "snapshot", "swap", "start", "smoke", "resume", "none"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			s, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.AcquireUpdate(); err != nil {
				t.Fatal(err)
			}
			r := ctlfiles.UpdateRecord{Schema: 1, ID: "attempt", Root: root, Socket: filepath.Join(root, "run/client.sock"), Phase: "draining", Candidate: filepath.Join(root, "system/updates/candidate"), Previous: filepath.Join(root, "system/updates/previous-attempt")}
			restored, resumed := false, false
			fail := func(stage string) error {
				if failure == stage {
					return errors.New("injected")
				}
				return nil
			}
			ops := ActivationOps{
				Drain: func(context.Context) error { return fail("drain") },
				Snapshot: func(context.Context) (string, error) {
					return filepath.Join(root, "backups/generation"), fail("snapshot")
				},
				Swap: func(context.Context) error { return fail("swap") },
				Smoke: func(context.Context) error {
					if err := fail("start"); err != nil {
						return err
					}
					current, _ := s.ReadUpdate()
					current.ReachedReady = true
					current.Phase = "ready"
					if err := s.WriteUpdate(*current); err != nil {
						return err
					}
					return fail("smoke")
				},
				Rollback: func(context.Context) error { restored = true; return nil },
				Resume:   func(context.Context) error { resumed = true; return fail("resume") },
			}
			got, err := Activate(t.Context(), s, r, ops)
			if failure == "none" {
				if err != nil || got.Phase != "complete" || !resumed || restored {
					t.Fatalf("%+v %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatal("failure accepted")
			}
			if failure == "smoke" || failure == "resume" {
				if restored || got.Phase != "held" || !got.ReachedReady {
					t.Fatalf("rolled back a ready candidate: %+v", got)
				}
			}
			if failure == "start" && (!restored || got.Phase != "rolled_back" || !resumed) {
				t.Fatalf("pre-ready failure did not restore: %+v", got)
			}
			if got.Cause == "" {
				t.Fatal("missing closed cause")
			}
		})
	}
}

func TestDiskFullAndForwardMigrationHold(t *testing.T) {
	for _, forward := range []bool{false, true} {
		t.Run(fmt.Sprint(forward), func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			s, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.AcquireUpdate(); err != nil {
				t.Fatal(err)
			}
			r := ctlfiles.UpdateRecord{Schema: 1, ID: "attempt", Root: root, Socket: filepath.Join(root, "run/client.sock"), Phase: "draining", Candidate: filepath.Join(root, "system/updates/candidate"), Previous: filepath.Join(root, "system/updates/previous-attempt"), ForwardMigration: forward}
			rollback := false
			ops := ActivationOps{Drain: func(context.Context) error { return nil }, Snapshot: func(context.Context) (string, error) { return filepath.Join(root, "backups/generation"), nil }, Swap: func(context.Context) error { return nil }, Smoke: func(context.Context) error { return syscall.ENOSPC }, Rollback: func(context.Context) error { rollback = true; return nil }, Resume: func(context.Context) error { return nil }}
			got, err := Activate(t.Context(), s, r, ops)
			if err == nil {
				t.Fatal("disk full accepted")
			}
			if forward {
				if rollback || got.Phase != "held" {
					t.Fatalf("irreversible candidate rolled back: %+v", got)
				}
			} else if !rollback || got.Cause != "update_disk_full" {
				t.Fatalf("same-schema disk failure did not restore: %+v", got)
			}
		})
	}
}
