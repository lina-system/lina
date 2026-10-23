// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"os"
	"strings"
	"testing"
)

// TestHeadless starts real Core with a synthetic installed identity and a
// private state root, and connects a generated-type JSON-RPC client to its Unix
// socket. A second process runs real lina tui with a controlling PTY at 80 by
// 24 cells. The test waits for the live connection-state code, sends an empty
// input through the protocol client without a configured model route, and
// checks the correlated capability_not_negotiated / refused reply. Ctrl-D and
// double Ctrl-C quit paths and terminal EOF restore the terminal. Another
// handshake and request prove that TUI quit leaves Core running. Core stops
// gracefully and removes its socket and run sentinel. The absent-Core case
// checks that the TUI creates no state.
func TestHeadless(t *testing.T) {
	t.Parallel()

	binary := buildLina(t)
	for _, quit := range []struct{ name, keys string }{{"quit", "\x04"}, {"interrupt", "\x03\x03"}, {"terminal EOF", "\x04"}} {
		t.Run(quit.name, func(t *testing.T) {
			h := newHarness(t, binary)
			core := h.startCore()
			client := h.client(core)
			term := h.startTUI()
			h.expectState(term, "live") // T-05's connection-state code, not prose.
			h.inputRefused(client)
			h.keys(term, quit.keys)
			// Keep the PTY open: the key, rather than closing a pipe, must quit.
			term.process.wait(t, 0)
			h.expectTerminalRestored(term)
			if term.process.stderr.Len() != 0 {
				t.Fatalf("TUI diagnostic: %s", &term.process.stderr)
			}
			// A new handshake and request prove that quitting did not stop Core.
			after := h.client(core)
			if after.welcome.Epoch != client.welcome.Epoch {
				t.Fatal("quitting TUI restarted Core")
			}
			h.inputRefused(after)
			h.stopCore(core)
		})
	}
	t.Run("Core absent", func(t *testing.T) {
		h := newHarness(t, binary)
		term := h.startTUI()
		term.process.wait(t, 69)
		h.expectTerminalRestored(term)
		for line := range term.lines {
			t.Errorf("absent Core produced terminal state %q", line)
		}
		entries, err := os.ReadDir(h.root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("TUI created state with Core absent: entries=%d error=%v", len(entries), err)
		}
	})
}

func TestStyledFailureIsPresentedOnce(t *testing.T) {
	t.Parallel()

	binary := buildLina(t)
	for _, combined := range []bool{true, false} {
		h := newHarness(t, binary)
		term := h.startTerminal([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}, combined)
		var output strings.Builder
		for line := range term.lines {
			output.WriteString(line)
		}
		term.process.wait(t, 69)
		h.expectTerminalRestored(term)
		if output.Len() == 0 || strings.Contains(output.String(), "lina tui:") {
			t.Fatalf("styled failure is absent or repeated as a CLI diagnostic: %q; stderr=%q", output.String(), term.process.stderr.String())
		}
		if !combined && term.process.stderr.Len() == 0 {
			t.Fatal("redirected stderr lost its diagnostic")
		}
	}
}
