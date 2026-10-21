// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestRestoreSurvivesProcessDeath(t *testing.T) {
	if root := os.Getenv("LINA_RESTORE_CRASH_ROOT"); root != "" {
		cfg, store := fixtureAt(t, root)
		material(t, store)
		m, err := Create(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
		cfg.Canon.Close(context.Background())
		cfg.Controls.(*ctlfiles.Store).Close()
		restoreTarget(t, root, "45")
		_, err = Restore(t.Context(), RestoreConfig{Root: root, Generation: filepath.Join(root, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations(), boundary: func(at string) error {
			if at == os.Getenv("LINA_RESTORE_CRASH_AT") {
				fmt.Fprintln(os.Stdout, "boundary")
				var release [1]byte
				_, err := os.Stdin.Read(release[:])
				return err
			}
			return nil
		}})
		t.Fatalf("restore unexpectedly completed: %v", err)
	}
	for _, stage := range []string{"copy_file", "saved:canon", "installed:canon"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRestoreSurvivesProcessDeath$")
			cmd.Env = append(os.Environ(), "LINA_RESTORE_CRASH_ROOT="+root, "LINA_RESTORE_CRASH_AT="+stage)
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
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || line != "boundary\n" {
				t.Fatalf("no crash boundary: %q %v %s", line, err, &diagnostic)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("restore survived kill")
			}
			pending := CheckRestore(root)
			if stage == "copy_file" {
				if pending != nil {
					t.Fatal(pending)
				}
			} else if !errors.Is(pending, ErrRestorePending) {
				t.Fatal("partial restore admitted Core")
			}
			entries, err := os.ReadDir(filepath.Join(root, "backups"))
			if err != nil {
				t.Fatal(err)
			}
			var generation string
			for _, entry := range entries {
				if entry.IsDir() {
					generation = filepath.Join(root, "backups", entry.Name())
				}
			}
			if _, err := Restore(t.Context(), RestoreConfig{Root: root, Generation: generation, Artifacts: map[string]string{"lina": "test"}, Migrations: canon.Migrations()}); err != nil {
				t.Fatal(err)
			}
			if err := CheckRestore(root); err != nil {
				t.Fatal(err)
			}
			if _, epoch, err := canon.Inspect(t.Context(), filepath.Join(root, "canon/canon.sqlite")); err != nil || epoch != 45 {
				t.Fatalf("epoch %d %v", epoch, err)
			}
		})
	}
}
