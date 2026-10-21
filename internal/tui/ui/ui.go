// SPDX-License-Identifier: Apache-2.0

// Package ui styles the connection screen from generated design tokens.
// Conversation input arrives with later increments.
package ui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Run reports live only after Core welcomes the connection. q or EOF closes
// this client; all other input is ignored and no conversation request is sent.
func Run(ctx context.Context, socket, productVersion string, input io.Reader, output io.Writer) error {
	display := newDisplay(output)
	if err := display.show(Connecting); err != nil {
		return err
	}
	client, err := clientapi.Dial(ctx, socket, productVersion)
	if err != nil {
		state := Failed
		cause := protocol.ErrorMalformed
		var refusal *clientapi.Refusal
		switch {
		case errors.Is(err, clientapi.ErrUnavailable):
			state = Stopped
			cause = protocol.ErrorUnavailable
		case errors.As(err, &refusal):
			state = Refused
			cause = refusal.Cause.Code
		}
		if writeErr := display.showStatus(ConnectionStatus{State: state, Cause: cause}); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return err
	}
	defer client.Close()
	if err := display.show(Live); err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	var gate term.Gate
	for {
		key, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return display.show(Quit)
		}
		if err != nil {
			return err
		}
		if gate.Admit(term.Key{Name: string(key)}, term.Toggle, time.Now()) && key == 'q' {
			return display.show(Quit)
		}
	}
}
