// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func installedAcceptanceStore(t *testing.T, h *harness) (*canon.Store, func()) {
	t.Helper()
	controls, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		controls.Close()
		t.Fatal(err)
	}
	epoch, err := controls.MintEpoch(0, 0)
	if err != nil {
		controls.Close()
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		controls.Close()
		t.Fatal(err)
	}
	var once sync.Once
	close := func() {
		once.Do(func() {
			if err := db.Close(context.Background()); err != nil {
				t.Error(err)
			}
			controls.Close()
		})
	}
	t.Cleanup(close)
	return db, close
}

func acceptanceStore(t *testing.T, root string, epoch uint64) (*canon.Store, func()) {
	t.Helper()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		controls.Close()
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		controls.Close()
		t.Fatal(err)
	}
	var once sync.Once
	close := func() {
		once.Do(func() {
			if err := db.Close(context.Background()); err != nil {
				t.Error(err)
			}
			controls.Close()
		})
	}
	t.Cleanup(close)
	return db, close
}

// Wait for an explicit child acknowledgement, then SIGKILL and reap it. The
// child blocks on stdin, so no elapsed-time guess selects the crash boundary.
func killAcceptanceChild(t *testing.T, h *harness, test string, env ...string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prior := h.binary
	h.binary = executable
	p := h.command("-test.run=^" + test + "$")
	h.binary = prior
	p.command.Env = append(p.command.Env, env...)
	p.command.Stdout = nil
	out, err := p.command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	p.start(t)
	ready := make(chan error, 1)
	go func() {
		var value bool
		err := json.NewDecoder(out).Decode(&value)
		if err == nil && !value {
			err = os.ErrInvalid
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			p.killAndWait()
			t.Fatalf("boundary: %v; %s", err, &p.stderr)
		}
	case <-h.ctx.Done():
		p.killAndWait()
		<-ready
		t.Fatal("crash boundary not reached")
	}
	p.killAndWait()
	if s, ok := p.command.ProcessState.Sys().(syscall.WaitStatus); !ok || s.Signal() != syscall.SIGKILL {
		t.Fatalf("expected SIGKILL: %v", p.err)
	}
}

func acknowledgeCrash(t *testing.T) {
	t.Helper()
	if err := json.NewEncoder(os.Stdout).Encode(true); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
	t.Fatal("parent did not kill acknowledged child")
}
