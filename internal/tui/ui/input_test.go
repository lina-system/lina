// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestTerminalInputCancellation(t *testing.T) {
	for _, input := range []string{"q", "\x03", "\x04", "cancel"} {
		t.Run(input, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx, stop, err := watchTerminalInput(parent, reader)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if stop != nil {
					stop()
				}
			}()
			want := errQuit
			if input == "cancel" {
				want = context.Canceled
				cancel()
			} else if _, err := io.WriteString(writer, "ignored"+input); err != nil {
				t.Fatal(err)
			}
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
				t.Fatal("input did not cancel the operation")
			}
			if !errors.Is(context.Cause(ctx), want) {
				t.Fatalf("input cause = %v, want %v", context.Cause(ctx), want)
			}
			err = stop()
			stop = nil
			if err != nil {
				t.Fatal(err)
			}
			// Cleanup joins the reader and releases only its cancellation
			// resources. The caller can still read its file without competition.
			if _, err := io.WriteString(writer, "after"); err != nil {
				t.Fatal(err)
			}
			if err := reader.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var remaining [5]byte
			if _, err := io.ReadFull(reader, remaining[:]); err != nil || string(remaining[:]) != "after" {
				t.Fatalf("input ownership after cleanup: %q, %v", remaining, err)
			}
		})
	}
}
