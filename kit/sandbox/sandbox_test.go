// SPDX-License-Identifier: Apache-2.0

package sandbox_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/sandbox"
)

func TestUnavailableNeverExecutes(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	l := lease.New(func() {})
	defer l.Release()
	root := t.TempDir()
	box := sandbox.New(owner, sandbox.Config{WorkDir: root})
	status := box.Probe(t.Context(), l)
	if status.Available || status.Cause == "" {
		t.Fatalf("status=%+v", status)
	}
	target := filepath.Join(root, "must-not-exist")
	err := box.Run(t.Context(), l, sandbox.Command{Path: "/bin/sh", Args: []string{"-c", `echo unsafe > "$1"`, "probe", target}})
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("run=%v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsandboxed fallback ran")
	}
}

func TestSandboxCanary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("verified OS sandbox not implemented")
	}
	launcher := "bwrap"
	if runtime.GOOS == "darwin" {
		launcher = "sandbox-exec"
	}
	if _, err := exec.LookPath(launcher); err != nil {
		t.Skip("OS launcher absent")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	for _, dir := range []string{work, filepath.Join(work, "nested/.git"), filepath.Join(root, "canon"), filepath.Join(root, "secrets")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"canon/canary", "secrets/canary", "outside", "work/nested/.git/config"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("protected-canary"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(root, "client.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if err := os.Symlink(filepath.Join(root, "canon"), filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	owner := proc.NewOwner()
	defer owner.Close(ctx)
	l := lease.New(func() {})
	defer l.Release()
	box := sandbox.New(owner, sandbox.Config{WorkDir: work, ReadDenied: []string{filepath.Join(root, "canon"), filepath.Join(root, "secrets"), socket}, Env: []string{"PATH=/bin:/usr/bin", "OPENAI_API_KEY=synthetic-secret", "GH_TOKEN=user-setup"}})
	status := box.Probe(ctx, l)
	if !status.Available {
		t.Fatalf("installed sandbox failed probe: %+v", status)
	}
	// Build an independent native canary; execute it through the public wrapper.
	binary := filepath.Join(root, "canary")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/canary")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build canary: %v %s", err, out)
	}
	for _, network := range []bool{false, true} {
		var output bytes.Buffer
		err := box.Run(ctx, l, sandbox.Command{Path: binary, Args: []string{root, tcp.Addr().String(), strconv.FormatBool(network)}, Network: network, Stdout: &output, Stderr: &output})
		if err != nil {
			t.Fatalf("canary network=%v: %v\n%s", network, err, &output)
		}
	}
}

func TestFirstCallReprobes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux launcher")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("OS launcher absent")
	}
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	l := lease.New(func() {})
	defer l.Release()
	root := t.TempDir()
	box := sandbox.New(owner, sandbox.Config{WorkDir: root})
	if status := box.Probe(t.Context(), l); !status.Available {
		t.Fatalf("probe=%+v", status)
	}
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	if err := box.Run(t.Context(), l, sandbox.Command{Path: "/bin/echo", Args: []string{"should not execute"}, Stdout: &output}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("run=%v", err)
	}
	if output.Len() != 0 || box.Status().Available || box.Status().Cause != "launcher_missing" {
		t.Fatalf("first call did not disable tools: %+v", box.Status())
	}
}

func TestProtectedPathOverridesWriteGrant(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"work", "canon", "canon/child"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "canon/child"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	l := lease.New(func() {})
	defer l.Release()
	for _, write := range []string{root, filepath.Join(root, "canon"), filepath.Join(root, "alias")} {
		box := sandbox.New(owner, sandbox.Config{WorkDir: filepath.Join(root, "work"), WriteRoots: []string{write}, ReadDenied: []string{filepath.Join(root, "canon")}})
		if status := box.Probe(t.Context(), l); status.Available || status.Cause != "invalid_policy" {
			t.Fatalf("overlapping grant accepted: %+v", status)
		}
	}
}
