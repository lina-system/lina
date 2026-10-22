// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

// The installed Core must complete a real TUI exchange even when its sandbox
// cannot start. Only the provider's HTTP/SSE boundary is scripted.
func TestM1SandboxUnavailable(t *testing.T) {
	data, err := os.ReadFile("m1/sandbox.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Cause, OS string
			LauncherExit    int `json:"launcher_exit"`
		}
		DiagnosticCode string `json:"diagnostic_code"`
		Input, Reply   string
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.OS != "" && tc.OS != runtime.GOOS {
				t.Skip("platform scenario")
			}
			var modelCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modelCalls.Add(1)
				var input struct{ Input []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(mustJSON(t, input.Input)), scenario.Input) {
					t.Error("TUI input did not reach the provider")
				}
				emitResponse(w, scenario.Reply, true)
			}))
			defer provider.Close()
			h, _ := installedConversationHarness(t, provider.URL)
			core := h.command("core", "--state-root", h.root, "--socket", h.socket)
			core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL="+provider.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
			if tc.LauncherExit != 0 {
				bin := filepath.Join(h.root, "bin")
				if err := os.Mkdir(bin, 0700); err != nil {
					t.Fatal(err)
				}
				// A failed executable, not a fake success standing in for isolation.
				if err := os.WriteFile(filepath.Join(bin, "bwrap"), []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexit "+strconv.Itoa(tc.LauncherExit)+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				for i, entry := range core.command.Env {
					if strings.HasPrefix(entry, "PATH=") {
						core.command.Env[i] = "PATH=" + bin
					}
				}
			}
			core.start(t)
			h.connect(core).Close()
			client, conversation := conversationClient(t, h)
			socket := h.socket
			tap := newAccountTap(t, h, socket)
			h.socket = tap.socket
			term := h.startScreenTUI("TERM=xterm-256color")
			h.socket = socket
			waitScreen(t, h, term, "Ctrl+D")
			// Live transport alone does not mean History has supplied the TUI's
			// destination. Observe its conversation subscription before typing.
			for {
				frame := tap.next(t, h)
				if snapshot, ok := frame.Payload.(protocol.Snapshot); ok && snapshot.Topic == "conversation:"+conversation {
					break
				}
			}
			h.keys(term, scenario.Input+"\r")
			waitScreen(t, h, term, scenario.Reply)
			for {
				frame, err := client.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
					break
				}
			}
			h.keys(term, "\x04")
			term.process.wait(t, 0)
			h.expectTerminalRestored(term)
			client.Close()
			// A fresh TUI must recover the completed exchange from Core's ledger.
			again := h.startScreenTUI("TERM=xterm-256color")
			waitScreen(t, h, again, scenario.Reply)
			h.keys(again, "\x04")
			again.process.wait(t, 0)
			h.expectTerminalRestored(again)
			h.stopCore(core)
			if got := modelCalls.Load(); got != 1 {
				t.Fatalf("model calls = %d, want one completed exchange", got)
			}
			found := false
			for _, line := range strings.Split(core.stderr.String(), "\n") {
				code, cause, ok := strings.Cut(line, ": ")
				if ok && code == scenario.DiagnosticCode && cause == tc.Cause {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing sandbox failure code %s: %s", tc.Cause, &core.stderr)
			}
		})
	}
}
