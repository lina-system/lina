// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

// At 120x34 the chip appears under the person's message that created an item,
// stays on one line, and Ctrl+B opens the move list. A second message bound to
// a fresh focus shows no chip.
func TestM2BindingChipInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "garden", "write_record", map[string]any{"id": "", "revision": "", "kind": "todo", "title": "Garden", "note": "Sow seeds."})
		case 2:
			assertAskResult(t, request.Input, "garden", "ok", "")
			emitResponse(w, "Saved the garden to-do.", true)
		case 3:
			emitResponse(w, "The garden to-do is on your list.", true)
		default:
			t.Error("unexpected model round")
			emitResponse(w, "Finished.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "COLORTERM=truecolor")
	waitComposerReady(t, h, terminal)
	chip := design.Text(design.English, "binding.chip")
	chip = strings.Replace(chip, "%s", "Garden", 1)
	h.keys(terminal, "Add a garden to-do.\r")
	snapshot := waitScreen(t, h, terminal, chip)
	rows := chipRows(snapshot, chip)
	if len(rows) != 1 {
		t.Fatalf("chip rows = %d, want exactly one:\n%s", len(rows), screenText(snapshot))
	}
	if cells := len(rows[0]); cells > 120 {
		t.Errorf("chip spans %d cells, want one line inside 120", cells)
	}
	// A message that continues the fresh focus shows no chip.
	h.keys(terminal, "Thanks.\r")
	continued := waitScreen(t, h, terminal, "on your list")
	if rows := chipRows(continued, chip); len(rows) != 1 {
		t.Fatalf("a fresh focus added a chip: %d rows\n%s", len(rows), screenText(continued))
	}
	// One tap stands for Ctrl+B in a terminal: the list of other candidates.
	h.keys(terminal, "\x02")
	list := waitScreen(t, h, terminal, design.Text(design.English, "binding.title"))
	text := screenText(list)
	for _, want := range []string{design.Text(design.English, "binding.new"), design.Text(design.English, "binding.none")} {
		if !strings.Contains(text, want) {
			t.Fatalf("move list missing %q:\n%s", want, text)
		}
	}
}

// chipRows returns the cells of each row that carries the chip.
func chipRows(snapshot tuiterm.Snapshot, chip string) [][]tuiterm.Cell {
	var rows [][]tuiterm.Cell
	for _, row := range snapshot.Cells {
		var text strings.Builder
		for _, cell := range row {
			text.WriteString(cell.Content)
		}
		if strings.Contains(text.String(), chip) {
			rows = append(rows, row)
		}
	}
	return rows
}
