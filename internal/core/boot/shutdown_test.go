// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestStopDuringSocketPublication(t *testing.T) {
	for _, step := range []boot.Step{boot.SocketCreated, boot.SocketBound, boot.SocketsBound} {
		for _, previous := range []bool{false, true} {
			t.Run(fmt.Sprintf("step-%d/previous-%t", step, previous), func(t *testing.T) {
				base := stateRoot(t)
				root := filepath.Join(base, "lina")
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				seedMain(t, root)
				socket := filepath.Join(base, "run/core.sock")
				old := filepath.Join(base, "run/previous.sock")
				if previous {
					crashAtSocket(t, root, old, boot.Ready)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				reached := false
				err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: socket, AfterStep: func(at boot.Step, _ boot.Snapshot) error {
					if at == step {
						// A successful connect precedes Serve and final sentinel
						// publication. Cancel at this exact boundary, without sleeps.
						conn, err := net.Dial("unix", socket)
						if err != nil {
							return err
						}
						if err := conn.Close(); err != nil {
							return err
						}
						reached = true
						cancel()
					}
					return nil
				}})
				if err != nil || !reached {
					t.Errorf("stop at socket boundary: reached=%t error=%v", reached, err)
				}
				for _, path := range []string{socket, old, filepath.Join(root, "system/core/run.sentinel")} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("runtime state remains after stop: %s: %v", path, err)
					}
				}
				controls, err := ctlfiles.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				defer controls.Close()
				if err := controls.Acquire(); err != nil {
					t.Fatal(err)
				}
				wantEpoch := uint64(1)
				if previous {
					wantEpoch = 2
				}
				if epoch, err := controls.Epoch(); err != nil || epoch != wantEpoch {
					t.Fatalf("lost minted epoch: %d %v", epoch, err)
				}
			})
		}
	}
}

func TestStopDuringPublicationPreservesRealFailure(t *testing.T) {
	root := stateRoot(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failure := errors.New("publication failure")
	err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step == boot.SocketBound {
			cancel()
			return errors.Join(ctx.Err(), failure)
		}
		return nil
	}})
	if !errors.Is(err, failure) {
		t.Fatalf("lost startup failure: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "system/core/run.sentinel")); err != nil {
		t.Fatalf("lost failure recovery record: %v", err)
	}
}

type shutdownFailureEvents struct{ err error }

func (e shutdownFailureEvents) SetEpoch(uint64) error       { return nil }
func (e shutdownFailureEvents) Close(context.Context) error { return e.err }

func TestStopDuringPublicationPreservesCleanupFailure(t *testing.T) {
	root := stateRoot(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failure := errors.New("event close failed")
	err := boot.Run(ctx, boot.Config{
		StateRoot: root, Socket: filepath.Join(root, "run/core.sock"),
		Services: boot.Services{OpenEvents: func(context.Context) (boot.Events, error) { return shutdownFailureEvents{failure}, nil }},
		AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step == boot.SocketCreated {
				cancel()
			}
			return nil
		},
	})
	if !errors.Is(err, failure) {
		t.Fatalf("lost cleanup failure: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "system/core/run.sentinel")); err != nil {
		t.Fatalf("lost failure recovery record: %v", err)
	}
}
