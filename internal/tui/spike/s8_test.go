// SPDX-License-Identifier: Apache-2.0

package spike_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"regexp"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/thisisjun786/lina/internal/tui/spike"
	"github.com/thisisjun786/lina/internal/tui/term"
)

var evidenceOutput = flag.String("s8-evidence", "", "write the measured S8 JSON record after assertions pass")

type s8Model struct {
	input  textarea.Model
	sealed string
}

func (m s8Model) Init() tea.Cmd { return nil }
func (m s8Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.Code == 'q' {
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
func (m s8Model) View() tea.View {
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, m.input.View(), m.sealed))
	v.AltScreen = true
	v.Cursor = m.input.Cursor()
	return v
}

func libraryPins(t *testing.T) map[string]string {
	t.Helper()
	source, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	pins := make(map[string]string)
	for _, name := range []string{"charm.land/bubbletea/v2", "charm.land/bubbles/v2", "charm.land/lipgloss/v2", "charm.land/glamour/v2", "github.com/charmbracelet/ultraviolet", "github.com/charmbracelet/x/vt"} {
		match := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s+(v\S+)`).FindSubmatch(source)
		if match == nil {
			t.Fatalf("missing pin: %s", name)
		}
		pins[name] = string(match[1])
	}
	return pins
}

// TestS8 uses actual textarea input, Lip Gloss composition, sealed-block Glamour
// output, Bubble Tea rendering, and VT parsing. Its waits observe output cells;
// the deadline only makes a broken run terminate. It does not measure IME or S9.
func TestS8(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	input := textarea.New()
	input.SetVirtualCursor(false)
	input.ShowLineNumbers = false
	input.Prompt = ""
	input.SetWidth(20)
	input.SetHeight(2)
	input.SetValue("한A")
	input.Focus()
	painter, err := glamour.NewTermRenderer(glamour.WithStandardStyle("notty"), glamour.WithWordWrap(0))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := painter.Render("**7**\n\n")
	if err != nil {
		t.Fatal(err)
	}
	h, err := term.Start(ctx, s8Model{input: input, sealed: sealed}, 20, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	first, err := h.Output.Wait(ctx, func(s term.Snapshot) bool {
		return s.AltScreen && s.Cells[0][0].Content == "한" && s.Cells[0][2].Content == "A" && s.CursorX == 3 && s.CursorY == 0 && !s.CursorHidden
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cells[0][0].Width != 2 {
		t.Fatal("Hangul cell width is not two")
	}
	h.Send(tea.PasteMsg{Content: "B"})
	last, err := h.Output.Wait(ctx, func(s term.Snapshot) bool {
		return s.Cells[0][3].Content == "B" && s.CursorX == 4 && s.CursorY == 0 && !s.CursorHidden
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range last.Cells[2:] {
		for _, cell := range row {
			found = found || cell.Content == "7"
		}
	}
	if !found {
		t.Fatal("sealed-block output missing from parsed screen")
	}
	h.Send(tea.KeyPressMsg{Code: 'q'})
	final, err := h.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if final.(s8Model).input.Value() != "한AB" {
		t.Fatal("paste did not reach the composer")
	}
	restored := h.Output.Snapshot()
	if restored.AltScreen || restored.CursorHidden || restored.Bytes == 0 || restored.Writes == 0 {
		t.Fatal("quit did not restore the terminal")
	}
	local := false
	record := spike.Record{
		Format: 1, Scenario: "S8",
		Terminal:  spike.Terminal{Name: "github.com/charmbracelet/x/vt (headless)", Version: libraryPins(t)["github.com/charmbracelet/x/vt"], TERM: "xterm-256color", SSH: &local},
		Libraries: libraryPins(t),
		Bounds:    map[string]json.RawMessage{"runs": []byte("true"), "quits": []byte("true"), "parses": []byte("true"), "sleep_calls": []byte("0")},
		Measured:  map[string]json.RawMessage{"runs": []byte("true"), "quits": []byte("true"), "parses": []byte("true"), "sleep_calls": []byte("0"), "hangul_cells": []byte("2"), "cursor_after_paste_cells": []byte("4")},
		Result:    "pass",
	}
	record.Measured["output_bytes"], _ = json.Marshal(restored.Bytes)
	record.Measured["output_writes"], _ = json.Marshal(restored.Writes)
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("S8: %s", data)
	if *evidenceOutput != "" {
		if err := os.WriteFile(*evidenceOutput, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordedEvidence(t *testing.T) {
	file, err := os.Open("../../../evidence/tui-spike/s8.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	record, err := spike.ReadRecord(file)
	if err != nil {
		t.Fatal(err)
	}
	for library, pin := range libraryPins(t) {
		if record.Libraries[library] != pin {
			t.Fatalf("evidence revision differs from go.mod: %s", library)
		}
	}
}
