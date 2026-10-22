// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2TUIHomeWaitingInstalledCore(t *testing.T) {
	t.Parallel()
	var terminal *terminal
	var outside, request string
	runCodexWorkTools(t, "../m2/tui-home-waiting.json", "", func(h *harness, phase string) {
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
			for _, row := range *overview.Waiting {
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
			waitScreen(t, h, terminal, "KEEP-DRAFT")
			h.keys(terminal, "\x10") // Ctrl+P proves the overview arrived.
			waitScreen(t, h, terminal, "Esc "+design.Text(design.English, "shortcuts.close"))
			waitScreen(t, h, terminal, request)
			h.keys(terminal, "\x1b")
			home := waitComposerReady(t, h, terminal)
			if !strings.Contains(screenText(home), request) || !strings.Contains(screenText(home), "KEEP-DRAFT") {
				t.Fatalf("Core waiting request missing from Home: %s", screenText(home))
			}
			if !strings.Contains(screenText(home), design.Text(design.English, "waiting")+" 2") {
				t.Fatalf("Home lost Core's waiting count: %s", screenText(home))
			}
		case "question":
			// The authenticated worker approval was answered by the harness. Its
			// next question must coexist with the overview-only outside-change row.
			waitScreen(t, h, terminal, "Which label should the worker use")
			if !strings.Contains(screenText(terminal.screen.Snapshot()), request) {
				t.Fatal("worker update hid the outside-change request")
			}
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
			home, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
				text := screenText(s)
				return strings.Contains(text, "KEEP-DRAFT") && !strings.Contains(text, request) &&
					!strings.Contains(text, design.Text(design.English, "waiting")+" 1")
			})
			if err != nil {
				t.Fatalf("resolved source retained on Home: %v; %s", err, screenText(home))
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
		}
	})
}
