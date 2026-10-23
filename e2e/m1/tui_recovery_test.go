// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/fakecore"
)

// Delay completion of the first welcome write until the PTY has displayed it.
// The disconnect fault then fires. No elapsed interval establishes ordering.
type observedWelcome struct {
	net.Conn
	ctx      context.Context
	observed <-chan struct{}
}

func (c observedWelcome) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err != nil {
		return n, err
	}
	select {
	case <-c.observed:
		return n, nil
	case <-c.ctx.Done():
		return n, c.ctx.Err()
	}
}

func TestM1TUIRecovery(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("tui-recovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Connections [][]fakecore.Step `json:"connections"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildLina(t))
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(h.socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	observed := make(chan struct{})
	fixtures := tuiFixtures(t)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		for index, steps := range scenario.Connections {
			clock := &scenarioClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			core, err := fakecore.New(fixtures, clock, steps)
			if err != nil {
				done <- err
				return
			}
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var stream io.ReadWriteCloser = conn
			if index == 0 {
				stream = observedWelcome{Conn: conn, ctx: ctx, observed: observed}
			}
			err = core.Run(ctx, stream)
			want := io.EOF // The second script waits for another request until client quit.
			if index == 0 {
				want = fakecore.ErrDisconnected
			}
			if !errors.Is(err, want) {
				done <- fmt.Errorf("connection %d: got %w, want %v", index, err, want)
				return
			}
		}
		done <- nil
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("fake recovery peer: %v", err)
		}
	})
	terminal := h.startTUI("TERM=xterm-256color", "NO_COLOR=1", "LC_ALL=C")
	expectCatalogState(t, h, terminal, design.English, "connecting")
	expectCatalogState(t, h, terminal, design.English, "live")
	close(observed)
	expectCatalogState(t, h, terminal, design.English, "retrying")
	// Epoch notices are outside conversation items and occur once per change.
	notices := 0
	notice := design.Text(design.English, "epoch_changed")
	live := design.Text(design.English, "live")
reconnected:
	for {
		select {
		case line, ok := <-terminal.lines:
			if !ok {
				t.Fatal("terminal closed before recovery")
			}
			if strings.Contains(line, notice) {
				notices++
			}
			if strings.HasSuffix(strings.TrimSpace(line), live) {
				break reconnected
			}
		case <-h.ctx.Done():
			t.Fatal("terminal did not recover")
		}
	}
	if notices != 1 {
		t.Fatalf("epoch notices = %d, want 1", notices)
	}
	h.keys(terminal, "q\n")
	expectCatalogState(t, h, terminal, design.English, "quit")
	terminal.process.wait(t, 0)
	for line := range terminal.lines {
		if strings.Contains(line, notice) {
			t.Fatal("replayed epoch notice")
		}
	}
	// Observe the peer's EOF before deferred cancellation can race its read.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fake recovery peer: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("fake recovery peer did not finish after client quit")
	}
}
