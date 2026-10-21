// SPDX-License-Identifier: Apache-2.0

package proc_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

func TestEnvironment(t *testing.T) {
	got := proc.Environment([]string{"PATH=/bin", "HOME=/home/example", "GH_TOKEN=synthetic-user-token", "OPENAI_API_KEY=synthetic-secret", "ANTHROPIC_AUTH_TOKEN=synthetic-secret", "GOOGLE_API_KEY=synthetic-secret", "CUSTOM_MODEL_TOKEN=synthetic-secret", "LD_PRELOAD=/untrusted.so", "PATH=/usr/bin", "broken"}, "CUSTOM_MODEL_TOKEN")
	want := []string{"GH_TOKEN=synthetic-user-token", "HOME=/home/example", "PATH=/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %q", got)
	}
}

func TestOwnerCancelsWholeGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	owner := proc.NewOwner()
	defer owner.Close(ctx)
	l := lease.New(func() {})
	defer l.Release()
	// Use an OS pipe passed directly to the child. exec.WaitDelay cannot close
	// the descendant's copy and manufacture EOF on the parent's reader.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- owner.Run(ctx, l, proc.Command{Path: "/bin/sh", Args: []string{"-c", "sleep 100 & echo ready; wait"}, Env: []string{"PATH=/bin:/usr/bin"}, Stdout: writer, Stderr: writer})
		writer.Close()
	}()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(reader).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("readiness = %q", line)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-ctx.Done():
		t.Fatal("descendant retained stdout after group kill")
	}
	eof := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); eof <- err }()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("descendant survived its process owner")
	}
	if err := owner.Run(ctx, l, proc.Command{Path: "/bin/true"}); !errors.Is(err, proc.ErrClosed) {
		t.Fatalf("run after close: %v", err)
	}
}

func TestLeaseRequired(t *testing.T) {
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	if err := owner.Run(t.Context(), lease.Lease{}, proc.Command{Path: "/bin/true"}); !errors.Is(err, lease.ErrInvalid) {
		t.Fatalf("missing lease: %v", err)
	}
}
