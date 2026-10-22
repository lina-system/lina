// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIActivityReconnectAcrossQueuedTurn(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		if n == 2 {
			if !strings.Contains(string(mustJSON(t, body.Input)), "Queued turn") {
				t.Error("queued input did not reach Core's provider")
			}
			emitPersonaCall(w, "queued-tool", "skill_index", struct{}{})
			return
		}
		text := "Old partial"
		if n == 3 {
			var result struct{ Type, Output string }
			if len(body.Input) == 0 || json.Unmarshal(body.Input[len(body.Input)-1], &result) != nil || result.Type != "function_call_output" {
				t.Error("queued tool did not return a result")
				return
			}
			var output struct{ Code string }
			if json.Unmarshal([]byte(result.Output), &output) != nil || output.Code != "ok" {
				t.Error("queued tool did not succeed")
			}
			text = "New partial"
		} else if n != 1 {
			t.Error("unexpected provider request")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", text)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(provider.Close)
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	observer, conversation := conversationClient(t, h)
	terminal, cut, resume := activityReconnectTUI(t, h)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "First turn\r")
	waitScreen(t, h, terminal, "Old partial")
	h.keys(terminal, "/queue Queued turn\r")
	for {
		e, err := observer.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := e.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == "Queued turn" {
			break
		}
	}
	h.keys(terminal, "\x1b")
	waitScreen(t, h, terminal, design.Text(design.English, "activity.stopping"))
	close(cut)
	waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
	// The first interrupt was lost with the transport. An independent client
	// stops the old turn while TUI reconnection is held behind the socket gate.
	if _, err := observer.Interrupt(h.ctx, protocol.Interrupt{ConversationID: &conversation}); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := observer.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if delta, ok := e.Payload.(protocol.OutputDelta); ok && delta.Text != nil && *delta.Text == "New partial" {
			break
		}
	}
	close(resume)
	screen := waitScreen(t, h, terminal, "New partial")
	activity := screenRows(screen)[30]
	if !strings.Contains(activity, design.Text(design.English, "activity.replying")) || !strings.Contains(activity, design.Text(design.English, "unknown")) {
		t.Fatalf("new turn retained old timing or stopping after same-Core reconnect: %q", activity)
	}
	if calls.Load() != 3 {
		t.Fatalf("provider requests=%d; want old turn, queued tool, queued reply", calls.Load())
	}
	// Esc must now reach Core and stop this new turn.
	h.keys(terminal, "\x1b")
	for {
		e, err := observer.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if turn, ok := e.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
			break
		}
	}
	t.Log("same-Core reconnect restored queued turn with unknown timing; real tool succeeded; Esc stopped new turn")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}

// Relay real Core frames unchanged. Drop the first interrupt with its transport
// and hold the next connection until Core has started the queued turn.
func activityReconnectTUI(t *testing.T, h *harness) (*terminal, chan struct{}, chan struct{}) {
	t.Helper()
	cut, resume := make(chan struct{}), make(chan struct{})
	socket := filepath.Join(h.root, "activity-reconnect.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	target := h.socket
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for connection := 0; connection < 2; connection++ {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			if connection > 0 {
				select {
				case <-resume:
				case <-ctx.Done():
					local.Close()
					return
				}
			}
			remote, err := (&net.Dialer{}).DialContext(ctx, "unix", target)
			if err != nil {
				local.Close()
				return
			}
			func() {
				stop := context.AfterFunc(ctx, func() { local.Close(); remote.Close() })
				defer stop()
				copied := make(chan struct{})
				go func() { defer close(copied); io.Copy(local, remote); local.Close() }()
				defer func() { local.Close(); remote.Close(); <-copied }()
				scanner := bufio.NewScanner(local)
				scanner.Buffer(make([]byte, 4096), 1<<20)
				for scanner.Scan() {
					var rpc struct{ Params protocol.Envelope }
					if err := json.Unmarshal(scanner.Bytes(), &rpc); err != nil {
						return
					}
					if connection == 0 && rpc.Params.Kind == protocol.KindInterrupt {
						select {
						case <-cut:
						case <-ctx.Done():
						}
						return
					}
					if _, err := remote.Write(append(scanner.Bytes(), '\n')); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); <-done })
	h.socket = socket
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=en_US.UTF-8"}, false, true, 100, 34)
	h.socket = target
	return terminal, cut, resume
}
