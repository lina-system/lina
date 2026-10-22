// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
)

func TestM1TUIWaitingEmptyInstalledCore(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		emitResponse(w, "WAITING-REPLY", true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	terminal, _ := h.startConversationTUI(100, 32, "TERM=xterm-256color", "LC_ALL=C")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "KEEP-DRAFT")
	waitScreen(t, h, terminal, "KEEP-DRAFT")
	empty := design.Text(design.English, "waiting.empty")
	for attempt := range 2 {
		h.keys(terminal, "\x17") // Ctrl+W
		// The pager footer is the redraw barrier, including on the buggy blank
		// screen. Check its body directly rather than waiting for a timeout.
		opened := waitScreen(t, h, terminal, "Esc "+design.Text(design.English, "shortcuts.close"))
		if strings.Count(screenText(opened), empty) != 1 {
			t.Fatalf("open %d: waiting pager lacks its empty state; cells=%s", attempt+1, screenText(opened))
		}
		h.keys(terminal, "\x1b")
		closed := waitComposerReady(t, h, terminal)
		if !strings.Contains(screenText(closed), "KEEP-DRAFT") || strings.Contains(screenText(closed), empty) {
			t.Fatal("closing waiting pager lost draft or leaked empty state into conversation")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("opening waiting pager submitted a model request")
	}
	// The restored composer still reaches the running Core and renders a reply.
	h.keys(terminal, "\r")
	replied := waitScreen(t, h, terminal, "WAITING-REPLY")
	if calls.Load() != 1 || strings.Contains(screenText(replied), empty) {
		t.Fatal("conversation after closing waiting pager failed or leaked empty state")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	waitCoreReady(t, h)
	h.stopCore(core)
}
