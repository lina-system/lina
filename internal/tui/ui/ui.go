// SPDX-License-Identifier: Apache-2.0

// Package ui styles the connection screen from generated design tokens.
// Conversation input arrives with later increments.
package ui

import (
	"bufio"
	"context"
	"errors"
	"io"

	"github.com/charmbracelet/x/term"
	"github.com/thisisjun786/lina/internal/clientapi"
)

// Run reports live only after Core welcomes the connection. q or EOF closes
// this client; all other input is ignored and no conversation request is sent.
func Run(ctx context.Context, socket, productVersion string, input io.Reader, output io.Writer) (runErr error) {
	display := newDisplay(output)
	// A real terminal must deliver keys immediately. Restore its original mode
	// on every return, including handshake failures and output errors.
	if file, ok := input.(interface{ Fd() uintptr }); ok && term.IsTerminal(file.Fd()) {
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
	}
	if err := display.show(Connecting); err != nil {
		return err
	}
	client, err := clientapi.Dial(ctx, socket, productVersion)
	if err != nil {
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
	if err := display.show(Live); err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	for {
		key, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return display.show(Quit)
		}
		if err != nil {
			return err
		}
		if key == 'q' || key == 3 || key == 4 { // q, Ctrl+C, Ctrl+D
			return display.show(Quit)
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
