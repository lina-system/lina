// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// This account-free regression exercises production install, Core, persistence
// and a 120x34 PTY. It does not stand in for the live-model or 24-hour acceptance.
func TestM1TUIChatPasteInstalledCore(t *testing.T) {
	t.Parallel()
	testTUIChatPasteInstalledCore(t, false)
}

func TestM1TUIChatPasteContinuationInstalledCore(t *testing.T) {
	t.Parallel()
	testTUIChatPasteInstalledCore(t, true)
}

func testTUIChatPasteInstalledCore(t *testing.T, split bool) {
	t.Helper()
	token := "ghp_" + strings.Repeat("ChatPasteRegression", 3)
	forbidden := token
	const suffix = "RemainingPrivateTokenSuffix"
	if split {
		forbidden = suffix
	}
	var calls atomic.Int32
	received, reply := make(chan struct{}), make(chan struct{})
	var replyOnce sync.Once
	release := func() { replyOnce.Do(func() { close(reply) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		body := string(mustJSON(t, req.Input))
		if strings.Contains(body, token) || strings.Contains(body, forbidden) {
			t.Error("pasted credential reached model or summary input")
		}
		if !strings.Contains(body, "오늘의 확인") {
			t.Error("ordinary Korean input was lost")
		}
		if calls.Add(1) == 1 {
			close(received)
		}
		select {
		case <-reply:
		case <-r.Context().Done():
			return
		}
		emitResponse(w, "민감한 값 없이 확인했어요.", true)
	}))
	defer provider.Close()
	defer release()
	h, start := installedConversationHarness(t, provider.URL)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	h.ctx = ctx
	core := start(true)
	c, _ := conversationClient(t, h)
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	chatPasteScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, token)
	if split {
		h.keys(terminal, "\x1b[200~오늘의 확인 "+token+"\x1b[201~")
		chatPasteScreen(t, h, terminal, "first-fragment", func(s string) bool { return strings.Contains(s, "[REDACTED]") }, token)
		h.keys(terminal, "\x1b[200~"+suffix+" 붙여넣기 끝\x1b[201~")
	} else {
		h.keys(terminal, "\x1b[200~오늘의 확인 "+token+" 붙여넣기 끝\x1b[201~")
	}
	chatPasteScreen(t, h, terminal, "pasted", func(s string) bool { return strings.Contains(s, "붙여넣기 끝") }, forbidden)
	h.keys(terminal, "\r")
	select {
	case <-received:
	case <-h.ctx.Done():
		t.Fatal("pasted input did not reach Core")
	}
	chatPasteScreen(t, h, terminal, "local-echo", func(s string) bool {
		return strings.Contains(s, "붙여넣기 끝") && strings.Contains(s, "[REDACTED]")
	}, forbidden)
	release()
	chatPasteScreen(t, h, terminal, "answered", func(s string) bool { return strings.Contains(s, "민감한 값 없이 확인했어요.") }, forbidden)
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	h.keys(terminal, "\x1b[A")
	chatPasteScreen(t, h, terminal, "recalled", func(s string) bool {
		rows := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		return len(rows) >= 2 && strings.Contains(rows[len(rows)-2], "붙여넣기 끝")
	}, forbidden)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	chatPasteAssertNoPersistedSecret(t, h.root, token)
	chatPasteAssertNoPersistedSecret(t, h.root, forbidden)

	core = start(true)
	// Reopen directly: startConversationTUI's one-use readiness link is retained.
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=ko"}, false, true, 120, 34)
	chatPasteScreen(t, h, terminal, "restored", func(s string) bool { return strings.Contains(s, "민감한 값 없이 확인했어요.") }, forbidden)
	if calls.Load() != 1 {
		t.Fatal("restart replayed the provider request")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.stopCore(core)
}

func chatPasteScreen(t *testing.T, h *harness, terminal *terminal, stage string, ready func(string) bool, forbidden string) tuiterm.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	snapshot, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool { return ready(screenText(s)) })
	if err != nil {
		t.Fatalf("chat paste screen %s did not arrive: %v", stage, err)
	}
	t.Log("observed screen:", stage)
	text, raw := screenText(snapshot), terminal.screen.Bytes()
	if forbidden != "" && (strings.Contains(text, forbidden) || bytes.Contains(raw, []byte(forbidden))) {
		t.Fatalf("chat paste screen %s exposed the synthetic credential", stage)
	}
	if directory := os.Getenv("LINA_CHAT_PASTE_CAPTURES"); directory != "" {
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

func chatPasteAssertNoPersistedSecret(t *testing.T, root, token string) {
	t.Helper()
	for _, area := range []string{"canon", "materials", "derived", "versions"} {
		err := filepath.WalkDir(filepath.Join(root, area), func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(token)) {
				return fmt.Errorf("synthetic credential persisted in %s", area)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
