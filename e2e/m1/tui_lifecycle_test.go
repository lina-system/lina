// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
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
			{name: "Ctrl+D", keys: "\x04"},
			{name: "Ctrl+C", keys: "\x03\x03"},
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

func TestTUIControlCConfirmation(t *testing.T) {
	binary := buildLina(t)
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expire), func(t *testing.T) {
			h := newHarness(t, binary)
			core := h.startCore()
			before := h.client(core)
			term := h.startScreenTUI("TERM=xterm-256color", "COLORTERM=truecolor")
			waitScreen(t, h, term, design.Text(design.English, "live"))
			h.keys(term, "\x03")
			// Synchronize on the actual rendered hint, never a sleep between keys.
			hint := design.Text(design.English, "shell.quit_hint")
			waitScreen(t, h, term, hint)
			select {
			case <-term.process.done:
				t.Fatal("first empty Ctrl+C exited")
			default:
			}
			if expire {
				// Expiry must redraw without another input or server notification.
				ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
				defer cancel()
				if _, err := term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
					return !strings.Contains(screenText(s), hint)
				}); err != nil {
					t.Fatalf("quit hint did not expire: %v", err)
				}
				h.keys(term, "\x03")
				waitScreen(t, h, term, hint)
				select {
				case <-term.process.done:
					t.Fatal("Ctrl+C after expiry exited instead of warning again")
				default:
				}
			}
			h.keys(term, "\x03")
			term.process.wait(t, 0)
			h.expectTerminalRestored(term)
			<-term.done
			if s := term.screen.Snapshot(); s.AltScreen || s.CursorHidden {
				t.Fatal("quit did not restore terminal screen/cursor")
			}
			if after := h.client(core); after.welcome.Epoch != before.welcome.Epoch {
				t.Fatal("TUI quit restarted Core")
			}
			h.stopCore(core)
		})
	}
}
