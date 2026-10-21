// SPDX-License-Identifier: Apache-2.0

package term

import (
	"context"
	"io"
	"os"
	"reflect"
	"testing"
)

func TestReadKeysPreservesEventsAndIgnoresPaste(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, "\x1b[200~q\x03\x1b[201~\x1b[113;1:3u\x1b[113;1:2u\x1b[113;1:1u")
		written <- err
		writer.Close()
	}()
	var keys []Key
	if err := ReadKeys(t.Context(), reader, func(key Key) bool { keys = append(keys, key); return true }); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	var events []KeyEvent
	for _, key := range keys {
		if key.Name != "q" {
			t.Fatalf("unexpected key %q", key.Name)
		}
		events = append(events, key.Event)
	}
	if !reflect.DeepEqual(events, []KeyEvent{Release, Repeat, Press}) {
		t.Fatalf("key events = %v", events)
	}
	if !keys[0].Events || !keys[1].Events {
		t.Fatal("explicit events lost before negotiation")
	}
}

func TestReadKeysStopsAndPreservesInputOwnership(t *testing.T) {
	for _, cancelInput := range []bool{false, true} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- ReadKeys(ctx, reader, func(Key) bool { return false }) }()
		if cancelInput {
			cancel()
		} else if _, err := io.WriteString(writer, "q"); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		cancel()
		// Completion joins the terminal reader. No EOF or pipe closure was used
		// to stop it, and its cancellation wrapper must not close our file.
		if _, err := io.WriteString(writer, "after"); err != nil {
			t.Fatal(err)
		}
		var text [5]byte
		if _, err := io.ReadFull(reader, text[:]); err != nil || string(text[:]) != "after" {
			t.Fatalf("input ownership: %q, %v", text, err)
		}
	}
}
