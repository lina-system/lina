// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

// Cleanup must reach descendants even when the direct child has already
// exited. Pipe EOF proves the grandchild actually closed its descriptors.
// This path also runs on macOS, which has no parent-death signal.
func TestHarnessProcessGroupCleanup(t *testing.T) {
	for _, leader := range []string{"wait", "exit 0"} {
		t.Run(leader, func(t *testing.T) {
			input, inputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer inputWriter.Close()
			ready, readyWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer ready.Close()
			defer readyWriter.Close()
			leaderInput, leaderWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer leaderInput.Close()
			defer leaderWriter.Close()
			if err := ready.SetReadDeadline(time.Now().Add(operationTimeout)); err != nil {
				t.Fatal(err)
			}
			var pid int
			t.Run("owner", func(t *testing.T) {
				h := newHarness(t, "/bin/sh")
				p := h.command("-c", `/bin/sh -c 'echo $$ >&4; read release <&3' & read leader <&5; `+leader)
				p.command.ExtraFiles = []*os.File{input, readyWriter, leaderInput}
				// Avoid stdout-copy goroutines delaying observation of an
				// exited group leader while its descendant holds the pipe.
				p.command.Stdout, p.command.Stderr = nil, nil
				p.start(t)
				readyWriter.Close()
				if _, err := fmt.Fscanln(ready, &pid); err != nil {
					t.Fatal(err)
				}
				// Let the leader exit only after the descendant is ready.
				// Otherwise correct group cleanup can beat its first write.
				if _, err := io.WriteString(leaderWriter, "continue\n"); err != nil {
					t.Fatal(err)
				}
				if leader == "exit 0" {
					p.wait(t, 0)
				}
			})
			if pid > 0 {
				defer syscall.Kill(pid, syscall.SIGKILL)
			}
			var marker [1]byte
			if _, err := ready.Read(marker[:]); err != io.EOF {
				t.Fatalf("descendant survived owner cleanup: %v", err)
			}
		})
	}
}
