// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestKillDuringCopyLeavesNoGeneration(t *testing.T) {
	if root := os.Getenv("LINA_BACKUP_CRASH_ROOT"); root != "" {
		cfg, _ := fixtureAt(t, root)
		if err := os.Mkdir(filepath.Join(root, "workspace"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "workspace/large"), bytes.Repeat([]byte("synthetic"), 32768), 0600); err != nil {
			t.Fatal(err)
		}
		cfg.boundary = func(stage string) error {
			if stage == "copy_file" {
				fmt.Fprintln(os.Stdout, "copying")
				var release [1]byte
				_, err := os.Stdin.Read(release[:])
				return err
			}
			return nil
		}
		if _, err := Create(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		t.Fatal("copy unexpectedly completed")
	}
	root := filepath.Join(t.TempDir(), "state")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKillDuringCopyLeavesNoGeneration$")
	cmd.Env = append(os.Environ(), "LINA_BACKUP_CRASH_ROOT="+root)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "copying\n" {
		t.Fatalf("copy boundary: %q %v %s", line, err, &diagnostic)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("process survived kill")
	}
	noGeneration(t, root)
	// The same state remains usable and an unpublished directory never counts.
	cfg, _ := fixtureAt(t, root)
	if _, err := Create(t.Context(), cfg); err != nil {
		t.Fatalf("backup after process death: %v", err)
	}
}
