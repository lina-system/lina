// SPDX-License-Identifier: Apache-2.0

// Package ui styles the connection screen from generated design tokens.
// Conversation input uses the negotiated client connection.
package ui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	terminalkeys "github.com/thisisjun786/lina/internal/tui/term"
)

// Run opens the negotiated conversation after welcome. Ctrl+D, an empty
// Ctrl+C or EOF closes only this client; connection-only screens also accept q.
func Run(ctx context.Context, socket, productVersion string, input io.Reader, output io.Writer) (runErr error) {
	display := newDisplay(output)
	terminal := false
	// A real terminal must deliver keys immediately. Restore its original mode
	// on every return, including handshake failures and output errors.
	if file, ok := input.(terminalInput); ok && term.IsTerminal(file.Fd()) {
		// Install signal handling before changing the mode, and keep it active
		// until after restoration. Signals cancel negotiation and blocked input.
		var stopSignals context.CancelFunc
		ctx, stopSignals = terminalSignalContext(ctx)
		defer stopSignals()
		previous, err := term.MakeRaw(file.Fd())
		if err != nil {
			return err
		}
		defer func() {
			if err := term.Restore(file.Fd(), previous); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}()
		if out, ok := output.(interface{ Fd() uintptr }); ok && term.IsTerminal(out.Fd()) {
			display.rawOutput = true
		}
		terminal = true
	}
	return runClientShell(ctx, socket, productVersion, input, display, terminal)
}

var errQuit = errors.New("TUI quit requested")

// Signals are a clean client exit; cancellation by the caller keeps its cause.
func terminalSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-signals:
			cancel(errQuit)
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(signals)
		cancel(context.Canceled)
		<-done
	}
}

// watchTerminalInput owns all terminal reads, including during negotiation.
// Closing its cancellation resources never closes the caller's input file.
func watchTerminalInput(ctx context.Context, input *os.File) (context.Context, func() error, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	var inputErr error // Read only after done closes.
	go func() {
		defer close(done)
		var gate terminalkeys.Gate
		inputErr = terminalkeys.ReadKeys(ctx, input, func(key terminalkeys.Key) bool {
			if !gate.Admit(key, terminalkeys.Toggle, time.Now()) {
				return true
			}
			return key.Name != "q" && key.Name != "ctrl+c" && key.Name != "ctrl+d"
		})
		if inputErr != nil {
			cancel(inputErr)
		} else {
			cancel(errQuit)
		}
	}()
	return ctx, func() error {
		cancel(context.Canceled)
		<-done // Join the sole reader before restoring the terminal mode.
		return inputErr
	}, nil
}

func readQuit(input io.Reader) error {
	reader := bufio.NewReader(input)
	var gate terminalkeys.Gate
	for {
		key, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if gate.Admit(terminalkeys.Key{Name: string(key)}, terminalkeys.Toggle, time.Now()) && (key == 'q' || key == 3 || key == 4) { // q, Ctrl+C, Ctrl+D
			return nil
		}
	}
}

type presentedError struct{ error }

func (e *presentedError) Unwrap() error { return e.error }

// ErrorShown reports whether the connection failure is already on screen and
// the diagnostic destination is also a terminal. Redirected diagnostics remain
// available, and any additional error (including restoring the terminal) is not
// considered presented.
func ErrorShown(err error, output io.Writer) bool {
	_, shown := err.(*presentedError)
	file, ok := output.(interface{ Fd() uintptr })
	return shown && ok && term.IsTerminal(file.Fd())
}
