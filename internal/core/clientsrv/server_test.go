// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListenPreservesExistingPaths(t *testing.T) {
	for _, kind := range []string{"file", "socket", "symlink", "public directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "core.sock")
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "absent"), path); err != nil {
					t.Fatal(err)
				}
			case "public directory":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
			}
			before, statErr := os.Lstat(path)
			listener, err := Listen(path)
			if err == nil {
				listener.Close()
				t.Fatal("accepted an unsafe or occupied socket path")
			}
			if statErr == nil {
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("existing path changed: %v", err)
				}
			} else {
				info, err := os.Stat(root)
				if err != nil || info.Mode().Perm() != 0755 {
					t.Fatalf("changed existing directory: %v", err)
				}
			}
		})
	}
}

func TestServeCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "core.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener) }()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Leave a partial frame pending to exercise cancellation of a blocked reader.
	if _, err := conn.Write([]byte(`{"jsonrpc":`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not finish on cancellation")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
}
