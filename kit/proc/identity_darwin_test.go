// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"os/exec"
	"testing"
	"time"
)

// A child that exited but has not been reaped stays in the process table. The
// cleanup sweep must classify it as a zombie instead of signalling it again,
// which requires the full short BSD info structure.
func TestProcessStatesClassifiesZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait() // reap after the assertion, so the record exists while checked
	deadline := time.Now().Add(10 * time.Second)
	for {
		states, err := processStates()
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if state.pid != cmd.Process.Pid {
				continue
			}
			if state.uniqueID == 0 || state.parentUniqueID == 0 {
				t.Fatalf("recorded identity of %d is incomplete: %+v", state.pid, state.processIdentity)
			}
			if state.zombie {
				return // The child has exited and is waiting to be reaped.
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d never appeared as an unreaped zombie", cmd.Process.Pid)
		}
		time.Sleep(time.Millisecond)
	}
}
