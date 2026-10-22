// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUISubmissionDisconnectInstalledCore(t *testing.T) {
	t.Parallel()
	const draft = "Keep this unsent draft"
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		call := calls.Add(1)
		want := "Earlier message"
		if call == 2 {
			want = draft + "x"
		}
		if call > 2 || !strings.Contains(string(mustJSON(t, body.Input)), want) {
			t.Error("unexpected model input or automatic replay")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emitResponse(w, "Submission recovery reply", true)
	}))
	t.Cleanup(provider.Close)
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	terminal, failWrite := submissionDisconnectTUI(t, h)
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	h.keys(terminal, "Earlier message\r")
	waitScreen(t, h, terminal, "Submission recovery reply")
	h.keys(terminal, draft)
	waitScreen(t, h, terminal, draft)

	// The proxy holds Core's EOF and closes only its request-reading half.
	// Enter must hit EPIPE before Receive can report the disconnect. There is
	// no timing window, sleep, fake reply or production fault hook here.
	core.killAndWait()
	failWrite()
	h.keys(terminal, "\r")
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	go func() {
		select {
		case <-terminal.process.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	snapshot, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		return strings.Contains(screenText(s), design.Text(design.English, "retrying"))
	})
	if err != nil {
		select {
		case <-terminal.process.done:
			t.Fatalf("submission disconnect terminated TUI: %v; stderr=%s", terminal.process.err, &terminal.process.stderr)
		default:
			t.Fatalf("submission disconnect did not retry: %v", err)
		}
	}
	if text := screenText(snapshot); !strings.Contains(text, draft) || !strings.Contains(text, "Submission recovery reply") {
		t.Fatal("retry lost draft or retained conversation")
	}
	core = start(true)
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	h.keys(terminal, "x")
	restored := waitScreen(t, h, terminal, draft+"x")
	if text := screenText(restored); calls.Load() != 1 || !strings.Contains(text, "Earlier message") || !strings.Contains(text, "Submission recovery reply") {
		t.Fatal("reconnect lost history or replayed input")
	}
	h.keys(terminal, "\r")
	_, err = terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return strings.Count(screenText(s), "Submission recovery reply") == 2
	})
	if err != nil || calls.Load() != 2 {
		t.Fatalf("explicit resubmission: calls=%d, error=%v", calls.Load(), err)
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}

// Relay one real Core connection, retaining its response half after EOF so the
// test can force the write-first ordering. Later connections go directly to Core.
func submissionDisconnectTUI(t *testing.T, h *harness) (*terminal, func()) {
	t.Helper()
	socket := h.socket
	proxy := filepath.Join(h.root, "submission.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: proxy, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	ready := make(chan struct{})
	accepted := make(chan *net.UnixConn, 1)
	done := make(chan struct{})
	var readyOnce sync.Once
	go func() {
		defer close(done)
		local, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer local.Close()
		remote, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return
		}
		defer remote.Close()
		stop := context.AfterFunc(ctx, func() { local.Close(); remote.Close() })
		defer stop()
		accepted <- local
		copied := make(chan struct{})
		go func() {
			defer close(copied)
			io.Copy(remote, local)
		}()
		defer func() { local.Close(); <-copied }()
		reader := bufio.NewReader(remote)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				// Intentionally withhold EOF until cleanup; the client's failed
				// write must close its own connection and unblock its receiver.
				<-ctx.Done()
				return
			}
			if _, err := local.Write(line); err != nil {
				return
			}
			var frame struct {
				Result protocol.Envelope `json:"result"`
			}
			if json.Unmarshal(line, &frame) == nil {
				if snapshot, ok := frame.Result.Payload.(protocol.Snapshot); ok && strings.HasPrefix(snapshot.Topic, "conversation:") {
					readyOnce.Do(func() { close(ready) })
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); <-done })
	h.socket = proxy
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "COLORTERM=truecolor", "LC_ALL=C"}, false, true, 120, 40)
	h.socket = socket
	var local *net.UnixConn
	select {
	case local = <-accepted:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	select {
	case <-ready:
	case <-h.ctx.Done():
		t.Fatal("conversation subscription missing")
	}
	listener.Close()
	if err := os.Symlink(socket, proxy); err != nil {
		t.Fatal(err)
	}
	return terminal, func() {
		if err := local.CloseRead(); err != nil {
			t.Fatal(err)
		}
	}
}
