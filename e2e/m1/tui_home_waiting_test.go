// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM2TUIHomeWaitingInstalledCore (m2/tui-home-waiting.json) adds a 120x36
// TUI to that worker journey. An outside record change appears on Home
// alongside the worker's authenticated decisions. Resolving the sources clears
// the summary and preserves the typed draft. The scenario checks parsed
// terminal cells on Home without opening the work panel, the worker-written
// artifact and cleanup. Each transition waits for its complete Home state.
func TestM2TUIHomeWaitingInstalledCore(t *testing.T) {
	t.Parallel()
	var terminal *terminal
	var outside, request string
	runCodexWorkTools(t, "../m2/tui-home-waiting.json", "", func(h *harness, phase string, questions []protocol.Question) {
		waitHome := func(requests ...string) {
			t.Helper()
			home, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
				text := screenText(s)
				if !s.AltScreen || s.CursorHidden || !strings.Contains(text, "KEEP-DRAFT") ||
					!strings.Contains(text, design.Text(design.English, "live")) {
					return false
				}
				waiting := design.Text(design.English, "waiting")
				if len(requests) == 0 {
					return !strings.Contains(text, waiting) && !strings.Contains(text, request)
				}
				if !strings.Contains(text, fmt.Sprintf("%s %d ", waiting, len(requests))) {
					return false
				}
				for _, request := range requests {
					if !strings.Contains(text, request) {
						return false
					}
				}
				return true
			})
			if err != nil {
				t.Fatalf("Home waiting state at %s: %v; %s", phase, err, screenText(home))
			}
		}
		switch phase {
		case "approval":
			// Only synthetic records in this harness are changed. Core detects the
			// outside edit and publishes its waiting row through the real socket.
			outside = filepath.Join(h.root, "versions", "records", "outside-fixture.md")
			if err := os.WriteFile(outside, []byte("Synthetic outside edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "home-reader", []protocol.Capability{{Name: "progress", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			mode := "overview"
			if _, err = c.Progress(h.ctx, protocol.ProgressGet{View: &mode}); err != nil {
				t.Fatal(err)
			}
			frame, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			overview, ok := frame.Payload.(protocol.Progress)
			if !ok || overview.Waiting == nil || len(*overview.Waiting) != 2 {
				t.Fatalf("missing approval/outside-change overview: %+v", frame)
			}
			var requests []string
			for _, row := range *overview.Waiting {
				requests = append(requests, row.Line.Sentence)
				if row.Ref.Source == "outside" {
					request = row.Line.Sentence
				}
			}
			if request == "" {
				t.Fatal("outside-change source absent")
			}
			terminal, _ = h.startConversationTUI(120, 36, "TERM=xterm-256color", "LC_ALL=C")
			waitComposerReady(t, h, terminal)
			h.keys(terminal, "KEEP-DRAFT")
			waitHome(requests...)
		case "question":
			// The authenticated worker approval was answered by the harness. Its
			// next question must coexist with the overview-only outside-change row.
			waitHome(questions[0].Card.Summary, request)
		case "completed":
			// The real stdio worker has written its verified fixture artifact.
			// Removing this test-owned file restores the original records tree.
			if err := os.Remove(outside); err != nil {
				t.Fatal(err)
			}
			c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "home-refresh", []protocol.Capability{{Name: "progress", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			topic := "progress"
			if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Receive(h.ctx); err != nil {
				t.Fatal(err)
			}
			waitHome()
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
		}
	})
}
