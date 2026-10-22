// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Production boot composition over installed state and the installed TUI must
// complete a conversation even when the explicitly selected launcher fails.
func TestM1SandboxUnavailable(t *testing.T) {
	data, err := os.ReadFile("m1/sandbox.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Input, Reply string
		Cases        []struct {
			Name, Cause, OS string
			LauncherExit    int `json:"launcher_exit"`
		}
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
			// Core runs in this test executable so the explicit launcher seam
			// remains in-process. Keep build verification enabled with exact
			// provenance for that executable; the TUI uses the installed CLI.
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			binary, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(h.root, "system/bin/manifest.json")
			provenance, err := manifest.Read(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			for i := range provenance.Binaries {
				if provenance.Binaries[i].Name == "lina" {
					provenance.Binaries[i].Size = int64(len(binary))
					provenance.Binaries[i].SHA256 = fmt.Sprintf("%x", sha256.Sum256(binary))
				}
			}
			if err := os.WriteFile(manifestPath, mustJSON(t, provenance), 0600); err != nil {
				t.Fatal(err)
			}
			route, err := modelpath.ResponsesFromEnv(func(key string) string {
				return map[string]string{"LINA_RESPONSES_BASE_URL": provider.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
			})
			if err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(h.root, "launcher")
			if tc.LauncherExit != 0 {
				if err := os.WriteFile(launcher, []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexit "+strconv.Itoa(tc.LauncherExit)+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(h.ctx)
			ready := make(chan boot.Snapshot, 1)
			done := make(chan error, 1)
			go func() {
				done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, SandboxLauncher: launcher, Conversation: route, AfterStep: func(step boot.Step, s boot.Snapshot) error {
					if step == boot.Ready {
						ready <- s
					}
					return nil
				}})
			}()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			select {
			case s := <-ready:
				if s.Sandbox.Available || s.Sandbox.Cause != tc.Cause {
					t.Fatalf("sandbox status: %+v", s.Sandbox)
				}
			case err := <-done:
				done <- err
				t.Fatalf("Core stopped before READY: %v", err)
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			client, conversation := conversationClient(t, h)
			defer client.Close()
			socket := h.socket
			tap := newAccountTap(t, h, socket)
			h.socket = tap.socket
			term := h.startScreenTUI("TERM=xterm-256color")
			h.socket = socket
			waitScreen(t, h, term, design.Text(design.English, "live"))
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
			if got := modelCalls.Load(); got != 1 {
				t.Fatalf("model calls = %d, want one completed exchange", got)
			}
			for _, locale := range []string{"en_US.UTF-8", "ko_KR.UTF-8"} {
				term := h.startScreenTUI("TERM=xterm-256color", "LC_ALL="+locale)
				notice := design.Text(design.LanguageFor(locale), "sandbox.unavailable")
				prefix := string([]rune(notice)[:min(18, len([]rune(notice)))])
				screen := screenText(waitScreen(t, h, term, prefix))
				if strings.Count(screen, prefix) != 1 {
					t.Errorf("setup notice is duplicated: %s", screen)
				}
				if strings.Contains(screen, "launcher_missing") || strings.Contains(screen, "probe_failed") {
					t.Error("private diagnostic leaked")
				}
				h.keys(term, "\x04")
				term.process.wait(t, 0)
				h.expectTerminalRestored(term)
			}
		})
	}
}

func TestM1SandboxEmptyPATH(t *testing.T) {
	h := newHarness(t, buildLina(t))
	dir := filepath.Join(h.root, "system/core")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	err := boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, AfterStep: func(step boot.Step, s boot.Snapshot) error {
		if step != boot.Ready {
			return nil
		}
		defer cancel()
		if !s.Sandbox.Available {
			return fmt.Errorf("empty PATH lost installed launcher: %s", s.Sandbox.Cause)
		}
		term := h.startScreenTUI("TERM=xterm-256color")
		waitScreen(t, h, term, "Connected")
		h.keys(term, "\x04")
		term.process.wait(t, 0)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}
