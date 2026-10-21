// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"golang.org/x/sys/unix"
)

type terminal struct {
	screen  *tuiterm.Recorder
	process *process
	master  *os.File
	lines   chan string
	done    chan struct{}
	err     error // Read after done closes.
	mode    unix.Termios
}

func (h *harness) startTUI(environment ...string) *terminal {
	return h.startTerminal(environment, false)
}

func (h *harness) startScreenTUI(environment ...string) *terminal {
	return h.startTerminalScreen(environment, false, true)
}
func (h *harness) startTerminal(environment []string, combined bool) *terminal {
	return h.startTerminalScreen(environment, combined, false)
}
func (h *harness) startTerminalScreen(environment []string, combined, screen bool) *terminal {
	h.t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		h.t.Fatalf("open pseudo-terminal: %v", err)
	}
	h.t.Cleanup(func() { master.Close() })
	name, err := slaveName(master)
	if err != nil {
		h.t.Fatalf("unlock pseudo-terminal: %v", err)
	}
	slave, err := os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	defer slave.Close()
	mode, err := unix.IoctlGetTermios(int(slave.Fd()), getTermios)
	if err != nil {
		h.t.Fatal(err)
	}
	// Start in canonical mode, as a shell would. The application must arrange
	// immediate key delivery and restore this mode when it exits.
	mode.Lflag |= unix.ICANON | unix.ECHO
	mode.Cc[unix.VEOF] = 4
	if err := unix.IoctlSetTermios(int(slave.Fd()), setTermios, mode); err != nil {
		h.t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		h.t.Fatal(err)
	}
	p := h.command("tui", "--socket", h.socket)
	p.command.Env = append(p.command.Env, environment...)
	p.command.Stdin, p.command.Stdout = slave, slave
	if combined {
		p.command.Stderr = slave
	}
	p.command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	p.start(h.t)
	term := &terminal{process: p, master: master, lines: make(chan string), done: make(chan struct{}), mode: *mode}
	if screen {
		term.screen, err = tuiterm.NewRecorder(80, 24)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	go func() {
		defer close(term.done)
		defer close(term.lines)
		if term.screen != nil {
			_, term.err = io.Copy(term.screen, master)
			if errors.Is(term.err, syscall.EIO) || errors.Is(term.err, os.ErrClosed) {
				term.err = nil
			}
			term.screen.Close()
			return
		}
		scanner := bufio.NewScanner(master)
		for scanner.Scan() {
			select {
			case term.lines <- strings.TrimSuffix(scanner.Text(), "\r"):
			case <-h.ctx.Done():
				return
			}
		}
		term.err = scanner.Err()
		// Linux reports EIO when the last slave closes; Darwin reports EOF.
		if errors.Is(term.err, syscall.EIO) || errors.Is(term.err, os.ErrClosed) {
			term.err = nil
		}
	}()
	h.t.Cleanup(func() {
		p.killAndWait()
		master.Close()
		// Drain any remaining lines so cleanup also joins a blocked sender.
		for range term.lines {
		}
		<-term.done
	})
	return term
}

func (h *harness) expectTerminalRestored(term *terminal) {
	h.t.Helper()
	mode, err := unix.IoctlGetTermios(int(term.master.Fd()), getTermios)
	if err != nil {
		h.t.Fatal(err)
	}
	if *mode != term.mode {
		h.t.Fatal("TUI did not restore the terminal mode")
	}
}

func (h *harness) expectState(term *terminal, state string) {
	h.t.Helper()
	timer := time.NewTimer(operationTimeout)
	defer timer.Stop()
	select {
	case line, ok := <-term.lines:
		if !ok {
			<-term.done
			h.t.Fatalf("terminal closed before state %s: %v", state, term.err)
		}
		if line != state {
			h.t.Fatalf("terminal state = %q, want %s", line, state)
		}
	case <-timer.C:
		h.t.Fatalf("terminal did not report state %s", state)
	case <-h.ctx.Done():
		h.t.Fatalf("terminal wait: %v", h.ctx.Err())
	}
}

func (h *harness) keys(term *terminal, keys string) {
	h.t.Helper()
	if err := term.master.SetWriteDeadline(time.Now().Add(operationTimeout)); err != nil {
		h.t.Fatal(err)
	}
	if _, err := io.WriteString(term.master, keys); err != nil {
		h.t.Fatalf("terminal input: %v", err)
	}
}
