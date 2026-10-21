// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProcessHostUsesInheritedSetupWithoutProviderSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool")
	write(t, path, []byte("#!/bin/sh\n[ -z \"$OPENAI_API_KEY\" ] || exit 99\n[ \"$LN91_TEST_SETUP\" = expected ] || exit 98\nprintf 'tool 1.2.3\\n'\n"), 0700)
	t.Setenv("PATH", dir)
	t.Setenv("OPENAI_API_KEY", "synthetic-secret")
	t.Setenv("LN91_TEST_SETUP", "expected")
	host := NewProcessHost()
	defer host.Close(context.Background())
	found, err := host.LookPath("tool")
	if err != nil || found != path {
		t.Fatalf("tool not discovered: %q %v", found, err)
	}
	out, err := host.Run(t.Context(), found, "--version")
	if err != nil || string(out) != "tool 1.2.3\n" {
		t.Fatalf("probe: %q %v", out, err)
	}
}

func TestProcessHostBoundsOutputAndHonorsCancellation(t *testing.T) {
	host := NewProcessHost()
	defer host.Close(context.Background())
	path := filepath.Join(t.TempDir(), "tool")
	write(t, path, []byte("#!/bin/sh\ni=0; while [ $i -lt 5000 ]; do printf x; i=$((i+1)); done\n"), 0700)
	out, err := host.Run(t.Context(), path)
	if err == nil || len(out) > 4096 {
		t.Fatalf("unbounded output: len=%d err=%v", len(out), err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	write(t, path, []byte("#!/bin/sh\ntouch \"$1\"\n"), 0700)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := host.Run(ctx, path, marker); err == nil {
		t.Fatal("started cancelled probe")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled command ran")
	}
}

func TestProcessHostDoesNotDiscoverRelativePATHEntries(t *testing.T) {
	t.Setenv("PATH", ".")
	host := NewProcessHost()
	defer host.Close(context.Background())
	if _, err := host.LookPath("tool"); err == nil {
		t.Fatal("accepted relative tool path")
	}
}
