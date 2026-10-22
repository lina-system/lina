// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"github.com/charmbracelet/x/ansi"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"testing"
)

// rawTerminalWriter ends each renderer write by restoring full-height margins
// and the cursor. PTY reads may split that write, so only sample its final boundary.
func renderedFrame(terminal *terminal, snapshot tuiterm.Snapshot) bool {
	raw := terminal.screen.Bytes()
	end := []byte(ansi.SaveCursor + ansi.SetTopBottomMargins(0, 0) + ansi.RestoreCursor)
	return snapshot.Bytes >= len(end) && snapshot.Bytes <= len(raw) && bytes.HasSuffix(raw[:snapshot.Bytes], end)
}

func TestRenderedFrameRejectsSplitPTYOutput(t *testing.T) {
	recorder, err := tuiterm.NewRecorder(20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	terminal := &terminal{screen: recorder}
	if _, err = recorder.Write([]byte("\x1b[?1049h\x1b[Hfirst row")); err != nil {
		t.Fatal(err)
	}
	if renderedFrame(terminal, recorder.Snapshot()) {
		t.Error("accepted a partial PTY frame")
	}
	end := ansi.SaveCursor + ansi.SetTopBottomMargins(0, 0) + ansi.RestoreCursor
	for i, b := range []byte(end) {
		if _, err = recorder.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
		if got := renderedFrame(terminal, recorder.Snapshot()); got != (i == len(end)-1) {
			t.Errorf("split frame at byte %d: complete=%v", i, got)
		}
	}
	complete := recorder.Snapshot()
	if _, err = recorder.Write([]byte("\x1b[Hnext")); err != nil {
		t.Fatal(err)
	}
	if renderedFrame(terminal, recorder.Snapshot()) {
		t.Error("accepted the next partial frame")
	}
	if !renderedFrame(terminal, complete) {
		t.Error("later bytes invalidated the earlier complete snapshot")
	}
}
