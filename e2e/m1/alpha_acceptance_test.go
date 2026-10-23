// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// This account-free regression exercises production install, Core, persistence
// and a 120x34 PTY. It does not stand in for the live-model or 24-hour acceptance.
func TestAlphaChatPasteInstalledCore(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("AlphaAcceptance", 3)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		body := string(mustJSON(t, req.Input))
		if strings.Contains(body, token) {
			t.Error("pasted credential reached model or summary input")
		}
		if !strings.Contains(body, "오늘의 확인") {
			t.Error("ordinary Korean input was lost")
		}
		calls.Add(1)
		emitResponse(w, "민감한 값 없이 확인했어요.", true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	h.ctx = ctx
	core := start(true)
	c, _ := conversationClient(t, h)
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	alphaScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, token)
	h.keys(terminal, "\x1b[200~오늘의 확인 "+token+" 붙여넣기 끝\x1b[201~")
	alphaScreen(t, h, terminal, "pasted", func(s string) bool { return strings.Contains(s, "붙여넣기 끝") }, token)
	h.keys(terminal, "\r")
	alphaScreen(t, h, terminal, "answered", func(s string) bool { return strings.Contains(s, "민감한 값 없이 확인했어요.") }, token)
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	alphaAssertNoPersistedSecret(t, h.root, token)

	core = start(true)
	// Reopen directly: startConversationTUI's one-use readiness link is retained.
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=ko"}, false, true, 120, 34)
	alphaScreen(t, h, terminal, "restored", func(s string) bool { return strings.Contains(s, "민감한 값 없이 확인했어요.") }, token)
	if calls.Load() != 1 {
		t.Fatal("restart replayed the provider request")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.stopCore(core)
}

func alphaScreen(t *testing.T, h *harness, terminal *terminal, stage string, ready func(string) bool, forbidden string) tuiterm.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	snapshot, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool { return ready(screenText(s)) })
	if err != nil {
		t.Fatalf("alpha screen %s did not arrive: %v", stage, err)
	}
	t.Log("observed screen:", stage)
	text, raw := screenText(snapshot), terminal.screen.Bytes()
	if forbidden != "" && (strings.Contains(text, forbidden) || bytes.Contains(raw, []byte(forbidden))) {
		t.Fatalf("alpha screen %s exposed the synthetic credential", stage)
	}
	if directory := os.Getenv("LINA_ALPHA_CAPTURES"); directory != "" {
		directory = filepath.Join(directory, t.Name())
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		frame := mustJSON(t, snapshot)
		for name, data := range map[string][]byte{stage + ".txt": []byte(text), stage + ".ansi": raw, stage + ".json": frame} {
			if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return snapshot
}

func alphaTurn(t *testing.T, h *harness, terminal *terminal, observer *clientapi.Client, input, reply, stage string) {
	t.Helper()
	h.keys(terminal, "\x1b[200~"+input+"\x1b[201~\r")
	for {
		frame, err := observer.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Error != nil {
			t.Fatal("alpha input refused:", frame.Error.Code)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	alphaScreen(t, h, terminal, stage, func(s string) bool { return strings.Contains(s, reply) }, "")
}
