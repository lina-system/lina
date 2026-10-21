// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTUITerminalLifecycle(t *testing.T) {
	binary := buildLina(t)
	for _, stage := range []string{"live", "handshake"} {
		for _, action := range []struct {
			name   string
			signal os.Signal
			keys   string
		}{
			{name: "SIGTERM", signal: syscall.SIGTERM},
			{name: "SIGINT", signal: syscall.SIGINT},
			{name: "SIGHUP", signal: syscall.SIGHUP},
			{name: "q", keys: "q"},
			{name: "Ctrl+C", keys: "\x03"},
		} {
			t.Run(stage+"/"+action.name, func(t *testing.T) {
				h := newHarness(t, binary)
				var core *process
				var listener *net.UnixListener
				if stage == "live" {
					core = h.startCore()
					h.client(core) // Wait for readiness through a real handshake.
				} else {
					if err := os.MkdirAll(filepath.Dir(h.socket), 0700); err != nil {
						t.Fatal(err)
					}
					var err error
					listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: h.socket, Net: "unix"})
					if err != nil {
						t.Fatal(err)
					}
					defer listener.Close()
					listener.SetDeadline(time.Now().Add(operationTimeout))
				}
				term := h.startTUI()
				if stage == "live" {
					h.expectState(term, "live")
				} else {
					conn, err := listener.Accept()
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					conn.SetReadDeadline(time.Now().Add(operationTimeout))
					// Receiving hello proves raw-mode setup and negotiation have
					// started. Keep the peer open without sending a welcome.
					if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
						t.Fatal(err)
					}
				}
				if action.signal != nil {
					if err := term.process.command.Process.Signal(action.signal); err != nil {
						t.Fatal(err)
					}
				} else {
					h.keys(term, action.keys)
				}
				// This is a liveness bound, not a sleep used for synchronization:
				// quitting must finish before the five-second handshake timeout.
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				select {
				case <-term.process.done:
				case <-timer.C:
					t.Fatal("quit did not interrupt the pending terminal operation")
				}
				h.expectTerminalRestored(term)
				term.process.wait(t, 0)
				if term.process.stderr.Len() != 0 {
					t.Fatalf("termination reported a failure: %s", &term.process.stderr)
				}
				if core != nil {
					h.client(core) // Termination must close only the TUI.
					h.stopCore(core)
				}
			})
		}
	}
}
