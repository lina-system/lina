// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/kit/protocol"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func TestM1TUIActivity(t *testing.T) {
	delta, finish := make(chan struct{}), make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) > 1 {
			select {
			case <-stop:
			case <-r.Context().Done():
			}
			return
		}
		select {
		case <-delta:
		case <-stop:
			return
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Activity reply\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-stop:
			return
		case <-r.Context().Done():
			return
		}
		emitResponse(w, "Activity reply", true)
	}))
	t.Cleanup(provider.Close)
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	interrupt := make(chan struct{})
	terminal := startReadyScreenTUI(t, h, []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LC_ALL=en_US.UTF-8"}, 100, 34, interrupt)
	waitScreen(t, h, terminal, "Ctrl+D")
	h.keys(terminal, "Show activity\r")
	ctx, cancel := context.WithTimeout(h.ctx, time.Second)
	defer cancel()
	thinking := design.Text(design.English, "activity.thinking")
	first, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		return strings.Contains(screenRows(s)[31], thinking+" (0s") && strings.Contains(screenText(s), "Show activity")
	})
	t.Logf("holding response (100x34):\n%s", strings.Join(screenRows(first), "\n"))
	if err != nil {
		t.Fatalf("activity row within 1s of Enter: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(h.ctx, 4*time.Second)
	defer cancel2()
	later, err := terminal.screen.Wait(ctx2, func(s tuiterm.Snapshot) bool {
		return strings.Contains(screenRows(s)[31], thinking+" (2s")
	})
	if err != nil {
		t.Fatalf("elapsed did not increase while provider held response: %v", err)
	}
	t.Logf("holding response after 2s (100x34):\n%s", strings.Join(screenRows(later), "\n"))
	close(delta)
	replying := waitScreen(t, h, terminal, design.Text(design.English, "activity.replying"))
	t.Logf("replying (100x34):\n%s", strings.Join(screenRows(replying), "\n"))
	close(finish)
	completed := waitScreen(t, h, terminal, strings.TrimSuffix(design.Text(design.English, "activity.worked_for"), "%s"))
	if strings.Contains(screenRows(completed)[31], "Esc") {
		t.Fatal("activity row survived completion")
	}
	t.Logf("completed (100x34):\n%s", strings.Join(screenRows(completed), "\n"))

	h.keys(terminal, "Hold for stop\r")
	waitScreen(t, h, terminal, thinking+" (1s")
	h.keys(terminal, "\x1b")
	stopping := waitScreen(t, h, terminal, design.Text(design.English, "activity.stopping"))
	t.Logf("stopping (100x34):\n%s", strings.Join(screenRows(stopping), "\n"))
	close(interrupt)
	stopped, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return !strings.Contains(screenRows(s)[31], design.Text(design.English, "activity.stop"))
	})
	if err != nil {
		t.Fatalf("stop did not end activity: %v", err)
	}
	// Explicitly observe an unchanged screen across several frame periods.
	idle, cancelIdle := context.WithTimeout(h.ctx, 350*time.Millisecond)
	defer cancelIdle()
	last, err := terminal.screen.Wait(idle, func(s tuiterm.Snapshot) bool { return s.Bytes != stopped.Bytes })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle output=%d bytes: %v", last.Bytes-stopped.Bytes, err)
	}
	t.Logf("PTY idle output across 350ms: %d bytes", last.Bytes-stopped.Bytes)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}

// Gate only transport delivery: every request still reaches the installed Core.
// Waiting on subscription admission avoids racing Enter against initial history;
// holding interrupt delivery makes the stopping frame observable without sleeps.
func startReadyScreenTUI(t *testing.T, h *harness, environment []string, width, height int, releaseInterrupt <-chan struct{}) *terminal {
	t.Helper()
	if releaseInterrupt == nil {
		terminal, _ := h.startConversationTUI(width, height, environment...)
		return terminal
	}
	// Probe admission before the interrupt-only proxy accepts the TUI.
	client, _ := conversationClient(t, h)
	client.Close()
	socket := h.socket
	proxy := socket + ".activity"
	listener, err := net.Listen("unix", proxy)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	ctx, cancel := context.WithCancel(h.ctx)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			// A reconnect may reach the proxy while Core is between sockets.
			// Keep that handshake pending until the real socket is available.
			var remote net.Conn
			for {
				remote, err = (&net.Dialer{}).DialContext(ctx, "unix", socket)
				if err == nil {
					break
				}
				select {
				case <-ctx.Done():
					local.Close()
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer local.Close()
				defer remote.Close()
				stopped := context.AfterFunc(ctx, func() { local.Close(); remote.Close() })
				defer stopped()
				done := make(chan struct{})
				go func() { defer close(done); io.Copy(local, remote); local.Close() }()
				defer func() { remote.Close(); <-done }()
				reader := bufio.NewReader(local)
				for {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						return
					}
					var rpc struct {
						Params protocol.Envelope `json:"params"`
					}
					if err := json.Unmarshal(line, &rpc); err != nil {
						return
					}
					if rpc.Params.Kind == protocol.KindInterrupt && releaseInterrupt != nil {
						select {
						case <-releaseInterrupt:
						case <-ctx.Done():
							return
						}
					}
					if _, err := remote.Write(line); err != nil {
						return
					}
					if request, ok := rpc.Params.Payload.(protocol.Subscribe); ok && request.Topic != nil && strings.HasPrefix(*request.Topic, "conversation:") {
						readyOnce.Do(func() { close(ready) })
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); workers.Wait() })
	h.socket = proxy
	terminal := h.startTerminalSize(environment, false, true, width, height)
	h.socket = socket
	select {
	case <-ready:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	return terminal
}
