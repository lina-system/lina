// SPDX-License-Identifier: Apache-2.0

package ui_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/tui/ui"
	"golang.org/x/sys/unix"
)

// Expose only Read and Fd: terminal input does not require Write, Close or Name.
type wrappedTerminalInput struct {
	file  *os.File
	reads atomic.Int32
}

func (r *wrappedTerminalInput) Fd() uintptr { return r.file.Fd() }
func (r *wrappedTerminalInput) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.file.Read(p)
}

func TestRunTerminalLifecycle(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, action := range []string{"q", "ctrl+c", "ctrl+d", "cancel"} {
			t.Run(fmt.Sprintf("wrapped=%t/%s", wrapped, action), func(t *testing.T) {
				master, slave, original := openInputTerminal(t)
				var input io.Reader = slave
				wrapper := &wrappedTerminalInput{file: slave}
				if wrapped {
					input = wrapper
				}
				cancel, done := runDuringHandshake(t, input)
				if action == "cancel" {
					cancel()
				} else {
					mode, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
					if err != nil {
						t.Fatal(err)
					}
					if mode.Lflag&(unix.ICANON|unix.ECHO) != 0 {
						t.Fatal("terminal remains in canonical/echo mode during negotiation")
					}
					keys := map[string]string{"q": "q", "ctrl+c": "\x03", "ctrl+d": "\x04"}
					if _, err := io.WriteString(master, keys[action]); err != nil {
						t.Fatal(err)
					}
				}
				// Hello synchronizes startup; this deadline only bounds shutdown.
				select {
				case err := <-done:
					if action == "cancel" {
						if !errors.Is(err, context.Canceled) {
							t.Errorf("caller cancellation = %v, want context.Canceled", err)
						}
					} else if err != nil {
						t.Errorf("quit = %v, want clean exit", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("shutdown did not interrupt negotiation and terminal input")
				}
				mode, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
				if err != nil || *mode != original {
					t.Fatalf("terminal mode was not restored: %v", err)
				}
				if wrapped && action != "cancel" && wrapper.reads.Load() == 0 {
					t.Fatal("input bypassed the caller's wrapper")
				}
				// The reader has been joined and the caller still owns its input.
				if _, err := io.WriteString(master, "after\n"); err != nil {
					t.Fatal(err)
				}
				if err := slave.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var remaining [6]byte
				if _, err := io.ReadFull(slave, remaining[:]); err != nil || string(remaining[:]) != "after\n" {
					t.Fatalf("input ownership after Run: %q, %v", remaining, err)
				}
			})
		}
	}
}

func TestRunCallerCancellationWithoutTerminal(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cancel, done := runDuringHandshake(t, reader)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller cancellation did not interrupt negotiation")
	}
}

func runDuringHandshake(t *testing.T, input io.Reader) (context.CancelFunc, <-chan error) {
	t.Helper()
	socket := filepath.Join(socketRoot(t), "core.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		done <- ui.Run(ctx, socket, "test", input, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-joined:
		case <-time.After(3 * time.Second):
			t.Error("Run did not join after cancellation")
		}
	})
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// A received hello proves Run has completed mode setup and is negotiating.
	// Hold the connection open without a welcome so cancellation must stop it.
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	return cancel, done
}

func openInputTerminal(t *testing.T) (*os.File, *os.File, unix.Termios) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	mode, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	mode.Lflag |= unix.ICANON | unix.ECHO
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, mode); err != nil {
		t.Fatal(err)
	}
	return master, slave, *mode
}
