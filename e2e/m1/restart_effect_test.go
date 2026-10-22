// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1RestartWaitsForEffect(t *testing.T) {
	h := newHarness(t, "")
	directory := filepath.Join(h.root, "system/core")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	registry := lifecycle.NewRegistry(nil)
	activity, err := registry.Acquire(h.ctx, lease.Activity{Class: lease.Hard, Reason: "synthetic effect"})
	if err != nil {
		t.Fatal(err)
	}
	defer activity.Release()
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- lease.Do(ctx, activity, func(ctx context.Context) error {
			activity.Release() // Lease release must not erase an in-flight call.
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	t.Cleanup(func() { cancel(); <-finished })
	select {
	case <-entered:
	case <-h.ctx.Done():
		t.Fatal("effect did not start")
	}
	core := &process{done: make(chan struct{})}
	go func() {
		core.err = boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, Activities: registry})
		close(core.done)
	}()
	t.Cleanup(func() { cancel(); <-core.done })
	c := restartClient(h, core)
	reply := c.call(t, protocol.KindRestartRequest, protocol.RestartRequest{})
	pending, ok := reply.Payload.(protocol.RestartStatus)
	if !ok || !pending.Owed || pending.RequestID == nil || reply.EffectState != protocol.EffectApplied {
		t.Fatalf("request: %+v", reply)
	}
	// A completed protocol round trip after the request proves the service stays
	// up while the effect is blocked; no sleep or timing-based negative assertion.
	reply = c.call(t, protocol.KindRestartGet, protocol.RestartGet{})
	status, ok := reply.Payload.(protocol.RestartStatus)
	if !ok || !status.Owed || status.RequestID == nil || *status.RequestID != *pending.RequestID || status.CommittedEpoch != nil {
		t.Fatalf("premature restart: %+v", reply)
	}
	close(release)
	select {
	case <-core.done:
	case <-h.ctx.Done():
		t.Fatal("restart did not finish after effect")
	}
	if !errors.Is(core.err, lifecycle.ErrRestart) {
		t.Fatalf("shutdown: %v", core.err)
	}
	for _, path := range []string{h.socket, filepath.Join(directory, "run.sentinel")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restart left %s: %v", filepath.Base(path), err)
		}
	}
}
