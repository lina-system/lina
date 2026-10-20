// SPDX-License-Identifier: Apache-2.0

// Package ui runs the plain-text connection screen. Terminal rendering and
// conversation input arrive with later increments.
package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/thisisjun786/lina/internal/clientapi"
)

// Run reports live only after Core welcomes the connection. q or EOF closes
// this client; all other input is ignored and no conversation request is sent.
func Run(ctx context.Context, socket, productVersion string, input io.Reader, output io.Writer) error {
	client, err := clientapi.Dial(ctx, socket, productVersion)
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := fmt.Fprintln(output, "live"); err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	for {
		key, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if key == 'q' {
			return nil
		}
	}
}
