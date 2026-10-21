// SPDX-License-Identifier: Apache-2.0

package rpc

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestWriterRejectsOversizeBeforeWriting(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	writer := NewWriter(server)
	if err := writer.Write(t.Context(), strings.Repeat("x", MaxFrameBytes)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal(err)
	}
	received := make(chan string, 1)
	go func() {
		line, err := NewReader(client).Read()
		if err != nil {
			received <- err.Error()
			return
		}
		received <- string(line)
	}()
	if err := writer.Write(t.Context(), map[string]bool{"ready": true}); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != "{\"ready\":true}\n" {
		t.Fatalf("bad framing after rejected write: %s", got)
	}
}

func TestWriterCancellationUnblocksAndPreservesNextWrite(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	writer := NewWriter(server)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := writer.Write(ctx, struct{}{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	received := make(chan error, 1)
	go func() { _, err := NewReader(client).Read(); received <- err }()
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := writer.Write(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
}
