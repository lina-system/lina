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
	"time"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
)

// The socket gate holds only the real Core's finish event, so even an instant
// fixture tool has a deterministic observable running frame. No event is
// invented, edited or reordered, and no sleep determines whether the test passes.
func toolRowsGate(t *testing.T, h *harness, release <-chan struct{}) (string, <-chan struct{}) {
	t.Helper()
	target := h.socket
	socket := filepath.Join(h.root, "tool-rows.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	ready := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		local, err := listener.Accept()
		if err != nil {
			return
		}
		defer local.Close()
		remote, err := net.Dial("unix", target)
		if err != nil {
			return
		}
		defer remote.Close()
		stop := context.AfterFunc(ctx, func() { local.Close(); remote.Close() })
		defer stop()
		copied := make(chan struct{})
		go func() { defer close(copied); io.Copy(remote, local); remote.Close() }()
		defer func() { local.Close(); remote.Close(); <-copied }()
		scanner := bufio.NewScanner(remote)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := append(append([]byte(nil), scanner.Bytes()...), '\n')
			var rpc struct{ Result, Params json.RawMessage }
			if json.Unmarshal(line, &rpc) != nil {
				return
			}
			raw := rpc.Result
			if len(raw) == 0 {
				raw = rpc.Params
			}
			var event struct {
				Kind    string
				Payload struct{ Status, Topic string }
			}
			json.Unmarshal(raw, &event)
			if event.Kind == "tool_activity" && event.Payload.Status == "finished" {
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
			}
			if _, err := local.Write(line); err != nil {
				return
			}
			if event.Kind == "snapshot" && strings.HasPrefix(event.Payload.Topic, "conversation:") {
				once.Do(func() { close(ready) })
			}
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); <-done })
	return socket, ready
}

func TestM1TUIToolRows(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run(map[bool]string{false: "unicode", true: "ascii"}[ascii], func(t *testing.T) {
			text := func(key string) string { return design.Text(design.English, "tool."+key) }
			listing, reading := tools.DisplayName("skill_index"), tools.DisplayName("read_skill")
			marks := design.Dark()
			runningMark, okMark, failedMark := marks.GlyphUnicodeToolRunning, marks.GlyphUnicodeToolOk, marks.GlyphUnicodeToolFailed
			if ascii {
				runningMark, okMark, failedMark = marks.GlyphAsciiToolRunning, marks.GlyphAsciiToolOk, marks.GlyphAsciiToolFailed
			}
			running := runningMark + " " + text("running") + "  " + listing
			ok := okMark + " " + text("ok") + "  " + listing
			failed := failedMark + " " + text("failed") + "  " + reading
			var requests atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Input []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				for _, raw := range request.Input {
					for _, forbidden := range []string{"tool_activity", listing, reading, running, ok, failed} {
						if strings.Contains(string(raw), forbidden) {
							t.Errorf("display row entered model history: %s", forbidden)
						}
					}
				}
				n := requests.Add(1)
				switch n {
				case 1:
					emitPersonaCall(w, "rows-ok", "skill_index", struct{}{})
				case 3:
					emitPersonaCall(w, "rows-failed", "read_skill", map[string]any{"id": "missing-fixture", "revision": 1, "commit": "missing"})
				case 2, 4:
					var item struct{ Type, Output string }
					var result struct{ Code string }
					if len(request.Input) == 0 {
						t.Error("missing actual tool result")
						return
					}
					json.Unmarshal(request.Input[len(request.Input)-1], &item)
					json.Unmarshal([]byte(item.Output), &result)
					want := "ok"
					reply := "Tool succeeded."
					if n == 4 {
						want = "failed"
						reply = "Tool failed as expected."
					}
					if item.Type != "function_call_output" || result.Code != want {
						t.Errorf("actual tool result: %s", item.Output)
					}
					emitResponse(w, reply, true)
				default:
					emitResponse(w, "History checked.", true)
				}
			}))
			defer provider.Close()
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			client, conversation := conversationClient(t, h)
			agent := client.Welcome.Agents[0].AgentID
			client.Close()
			release := make(chan struct{})
			var released sync.Once
			defer released.Do(func() { close(release) })
			socket, ready := toolRowsGate(t, h, release)
			original := h.socket
			h.socket = socket
			env := []string{"TERM=xterm-256color", "LC_ALL=en_US.UTF-8"}
			if ascii {
				env = append(env, "LINA_GLYPHS=ascii")
			}
			terminal := h.startTerminalSize(env, false, true, 100, 34)
			h.socket = original
			select {
			case <-ready:
			case <-h.ctx.Done():
				t.Fatal("TUI not subscribed")
			}
			waitComposerReady(t, h, terminal)
			h.keys(terminal, "List fixture skills\r")
			ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
			screen, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool { return strings.Contains(screenText(s), running) })
			cancel()
			if err != nil {
				t.Fatalf("missing running call row: %v\n%s", err, screenText(screen))
			}
			t.Logf("observed running: %s", running)
			released.Do(func() { close(release) })
			waitScreen(t, h, terminal, ok)
			waitScreen(t, h, terminal, "Tool succeeded.")
			h.keys(terminal, "Read missing fixture skill\r")
			waitScreen(t, h, terminal, failed)
			waitScreen(t, h, terminal, "Tool failed as expected.")
			t.Logf("observed completion: %s; %s", ok, failed)
			// A later turn rebuilds the request from stored conversation history.
			client, conversation = conversationClient(t, h)
			admittedTurn(t, h, client, conversation, "rows-history", "Check history.")
			if requests.Load() != 5 {
				t.Fatalf("requests=%d", requests.Load())
			}
			p := h.command("export", "--socket", h.socket, "--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
			p.start(t)
			p.wait(t, 0)
			var exported protocol.ExportResult
			if err := json.Unmarshal(p.stdout.Bytes(), &exported); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(exported.Directory, "episodes.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"tool_activity", listing, reading, running, ok, failed} {
				if strings.Contains(string(data), forbidden) {
					t.Errorf("display row exported: %s", forbidden)
				}
			}
			if !strings.Contains(string(data), "Tool succeeded.") || !strings.Contains(string(data), "Tool failed as expected.") {
				t.Fatal("export missing real conversation text")
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			client.Close()
			h.stopCore(core)
		})
	}
}
