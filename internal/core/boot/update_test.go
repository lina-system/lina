// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/recover"
)

func TestInterruptedUpdateRefusesRealBootBeforeEpochAndCanon(t *testing.T) {
	for _, phase := range []string{"draining", "drained", "snapshot", "swapping", "starting", "booting", "ready", "rollback", "held"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			s, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Acquire(); err != nil {
				t.Fatal(err)
			}
			s.Write("main.json", []byte(`{"main_id":"update-test"}`))
			s.Write("epoch", []byte("42"))
			r := ctlfiles.UpdateRecord{Schema: 1, ID: "attempt", Root: root, Socket: filepath.Join(root, "run/client.sock"), Phase: phase, Candidate: filepath.Join(root, "system/updates/candidate"), Previous: filepath.Join(root, "system/updates/previous-attempt"), ReachedReady: phase == "ready"}
			if err = s.WriteUpdate(r); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if err = Run(t.Context(), Config{StateRoot: root, Socket: r.Socket}); !errors.Is(err, recover.ErrUpdateHeld) {
				t.Fatalf("unfinished update booted: %v", err)
			}
			epoch, err := os.ReadFile(filepath.Join(root, "system/core/epoch"))
			if err != nil || string(epoch) != "42" {
				t.Fatal("held boot minted epoch", err)
			}
			if _, err = os.Lstat(filepath.Join(root, "canon")); !os.IsNotExist(err) {
				t.Fatal("held boot opened canon")
			}
		})
	}
}
