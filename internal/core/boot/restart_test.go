// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/lease"
)

type closeFailure struct{}

func (closeFailure) SetEpoch(uint64) error       { return nil }
func (closeFailure) Close(context.Context) error { return os.ErrPermission }

func TestRestartClosesServicesBeforeReturningPlannedExit(t *testing.T) {
	for _, failClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "close failure"}[failClose], func(t *testing.T) {
			root := stateRoot(t)
			var restart *lifecycle.Restarter
			var trace []string
			cfg := boot.Config{NoSync: true, StateRoot: root, Socket: filepath.Join(root, "run/core.sock")}
			cfg.Services.Lifecycle = func(r *lifecycle.Restarter) { restart = r }
			cfg.Services.OpenEvents = func(context.Context) (boot.Events, error) {
				if failClose {
					return closeFailure{}, nil
				}
				return testEvents{&trace}, nil
			}
			cfg.AfterStep = func(step boot.Step, _ boot.Snapshot) error {
				if step == boot.Ready {
					_, err := restart.Request(t.Context(), "")
					return err
				}
				return nil
			}
			err := boot.Run(t.Context(), cfg)
			if failClose {
				if !errors.Is(err, os.ErrPermission) || errors.Is(err, lifecycle.ErrRestart) {
					t.Fatalf("cleanup failure claimed planned exit: %v", err)
				}
			} else {
				if !errors.Is(err, lifecycle.ErrRestart) {
					t.Fatalf("restart: %v", err)
				}
				if trace[len(trace)-1] != "close events" {
					t.Fatal("returned before service cleanup")
				}
				if _, err := os.Lstat(filepath.Join(root, "system/core/run.sentinel")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("sentinel remains: %v", err)
				}
			}
			if _, err := os.Lstat(cfg.Socket); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("socket remains: %v", err)
			}
		})
	}
}

func TestBootUsesTheSharedRegistryForRestart(t *testing.T) {
	root := stateRoot(t)
	r := lifecycle.NewRegistry(nil)
	activity, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "fake effect"})
	if err != nil {
		t.Fatal(err)
	}
	defer activity.Release()
	var restart *lifecycle.Restarter
	cfg := boot.Config{NoSync: true, StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), Activities: r}
	cfg.Services.Lifecycle = func(x *lifecycle.Restarter) { restart = x }
	cfg.AfterStep = func(step boot.Step, _ boot.Snapshot) error {
		if step != boot.Ready {
			return nil
		}
		return lease.Do(t.Context(), activity, func(context.Context) error {
			activity.Release()
			if _, err := restart.Request(t.Context(), "drain"); err != nil {
				return err
			}
			if exit, report, err := restart.Step(t.Context()); exit || err != nil || !report.Busy() {
				t.Fatalf("in-flight boot effect: %v %+v %v", exit, report, err)
			}
			return nil
		})
	}
	if err := boot.Run(t.Context(), cfg); !errors.Is(err, lifecycle.ErrRestart) {
		t.Fatalf("after effect finished: %v", err)
	}
}
