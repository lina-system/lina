// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestRemovalCrashProcess(t *testing.T) {
	if root := os.Getenv("LINA_RETRACT_CRASH_ROOT"); root != "" {
		db, _ := openGeneration(t, root)
		seedProtocol(t, db)
		s := protocolStore(t, db, root)
		s.fault = func(stage string) error {
			if stage == os.Getenv("LINA_RETRACT_CRASH_STAGE") {
				os.Exit(79)
			}
			return nil
		}
		if err := s.Delete(t.Context(), Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "memory-1"}}}); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash boundary was not reached")
	}
	for _, stage := range []string{"after_sync", "after_apply"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRemovalCrashProcess$")
			cmd.Env = append(os.Environ(), "LINA_RETRACT_CRASH_ROOT="+root, "LINA_RETRACT_CRASH_STAGE="+stage)
			output, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 79 {
				t.Fatalf("crash process: %v\n%s", err, output)
			}
			db, _ := openGeneration(t, root)
			// Inspect privately before publication; SQLite must have rolled everything
			// back even when the child died after applying all statements.
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				var targets, applied int
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_target").Scan(&targets); err != nil {
					return err
				}
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM removal_applied").Scan(&applied); err != nil {
					return err
				}
				if targets != 0 || applied != 0 {
					t.Fatalf("partial commit after death: targets=%d applied=%d", targets, applied)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s := protocolStore(t, db, root)
			if visibleProtocol(t, db) != 0 {
				t.Fatal("durable deletion was lost at process death")
			}
			v, err := s.Snapshot(t.Context(), "person-1", "agent-1")
			if err != nil {
				t.Fatal(err)
			}
			if v.Visible(Ref{Kind: Memory, ID: "memory-1"}) || v.CanExtract([]string{"event-1"}, nil) {
				t.Fatal("replayed tombstone did not suppress reuse")
			}
		})
	}
}
