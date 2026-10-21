// SPDX-License-Identifier: Apache-2.0

// Package ui styles the connection screen from generated design tokens.
// Conversation input arrives with later increments.
package ui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/term"
	"github.com/thisisjun786/lina/internal/clientapi"
)

// Run reports live only after Core welcomes the connection. q, Ctrl+C, Ctrl+D
// or EOF closes this client; no conversation request is sent.
func Run(ctx context.Context, socket, productVersion string, input io.Reader, output io.Writer) (runErr error) {
	display := newDisplay(output)
	terminal := false
	// A real terminal must deliver keys immediately. Restore its original mode
	// on every return, including handshake failures and output errors.
	if file, ok := input.(*os.File); ok && term.IsTerminal(file.Fd()) {
		// Install signal handling before changing the mode, and keep it active
		// until after restoration. Signals cancel negotiation and blocked input.
		var stopSignals context.CancelFunc
		ctx, stopSignals = signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
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
		var stopInput func() error
		ctx, stopInput, err = watchTerminalInput(ctx, file)
		if err != nil {
			return err
		}
		defer func() {
			if err := stopInput(); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}()
		terminal = true
	}
	if err := display.show(Connecting); err != nil {
		return err
	}
	client, err := clientapi.Dial(ctx, socket, productVersion)
	if err != nil {
		if terminal && ctx.Err() != nil {
			return finishTerminalInput(ctx, display)
		}
		state := Failed
		var refusal *clientapi.Refusal
		switch {
		case errors.Is(err, clientapi.ErrUnavailable):
			state = Stopped
		case errors.As(err, &refusal):
			state = Refused
		}
		if writeErr := display.show(state); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		if display.interactive {
			return &presentedError{err}
		}
		return err
	}
	defer client.Close()
	if terminal && ctx.Err() != nil {
		return finishTerminalInput(ctx, display)
	}
	if err := display.show(Live); err != nil {
		return err
	}
	if terminal {
		<-ctx.Done()
		return finishTerminalInput(ctx, display)
	}
	if err := readQuit(input); err != nil {
		return err
	}
	return display.show(Quit)
}

var errQuit = errors.New("TUI quit requested")

// watchTerminalInput owns all terminal reads, including during negotiation.
// Closing its cancellation resources never closes the caller's input file.
func watchTerminalInput(ctx context.Context, input *os.File) (context.Context, func() error, error) {
	reader, err := uv.NewCancelReader(input)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := readQuit(reader); err != nil {
			cancel(err)
		} else {
			cancel(errQuit)
		}
	}()
	return ctx, func() error {
		reader.Cancel()
		<-done // Join the sole reader before restoring the terminal mode.
		return reader.Close()
	}, nil
}

func finishTerminalInput(ctx context.Context, display *display) error {
	cause := context.Cause(ctx)
	if errors.Is(cause, errQuit) || errors.Is(cause, context.Canceled) {
		return display.show(Quit)
	}
	return cause
}

func readQuit(input io.Reader) error {
	reader := bufio.NewReader(input)
	for {
		key, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if key == 'q' || key == 3 || key == 4 { // q, Ctrl+C, Ctrl+D
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
