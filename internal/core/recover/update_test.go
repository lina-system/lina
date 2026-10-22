// SPDX-License-Identifier: Apache-2.0
package recover

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestUpdateRecoveryNeverRollsBackAfterReadyOrForwardMigration(t *testing.T) {
	for _, tc := range []struct {
		phase          string
		ready, forward bool
		want           string
	}{
		{"drained", false, false, "previous"}, {"snapshot", false, false, "previous"},
		{"swapping", false, false, "rollback"}, {"starting", false, false, "rollback"},
		{"booting", false, false, "rollback"}, {"ready", true, false, "hold"},
		{"booting", false, true, "hold"}, {"complete", true, false, "resume"},
	} {
		t.Run(tc.phase+string(rune('0'+boolInt(tc.forward))), func(t *testing.T) {
			r := ctlfiles.UpdateRecord{Phase: tc.phase, ReachedReady: tc.ready, ForwardMigration: tc.forward}
			if got := UpdateAction(r); got != tc.want {
				t.Fatalf("%+v: got %s want %s", r, got, tc.want)
			}
		})
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestPendingUpdateClosesCoreBeforeAnyEpoch(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	s, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Acquire(); err != nil {
		t.Fatal(err)
	}
	r := ctlfiles.UpdateRecord{Schema: 1, ID: "attempt", Root: root, Socket: filepath.Join(root, "run/client.sock"), Phase: "swapping", Candidate: filepath.Join(root, "system/updates/candidate"), Previous: filepath.Join(root, "system/updates/previous-attempt")}
	if err = s.WriteUpdate(r); err != nil {
		t.Fatal(err)
	}
	if err = UpdateStart(s, root, r.Socket); !errors.Is(err, ErrUpdateHeld) {
		t.Fatalf("pending update admitted: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "system/core/epoch")); !os.IsNotExist(err) {
		t.Fatal("guard minted epoch")
	}
}

func TestMovedRootHoldsEvenACompletedUpdate(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	s, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Acquire(); err != nil {
		t.Fatal(err)
	}
	r := ctlfiles.UpdateRecord{Schema: 1, ID: "attempt", Root: root, Socket: filepath.Join(root, "run/client.sock"), Phase: "complete", Candidate: filepath.Join(root, "system/updates/candidate"), Previous: filepath.Join(root, "system/updates/previous-attempt"), ReachedReady: true}
	if err = s.WriteUpdate(r); err != nil {
		t.Fatal(err)
	}
	if err = UpdateStart(s, root+"-moved", r.Socket); !errors.Is(err, ErrUpdateHeld) {
		t.Fatalf("path change admitted: %v", err)
	}
}
