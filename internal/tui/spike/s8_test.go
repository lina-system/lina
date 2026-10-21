// SPDX-License-Identifier: Apache-2.0

package spike_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"regexp"
	"sync"
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
		// Release the application cursor in the final View so Bubble Tea shows
		// the main-screen cursor on close even after an empty startup flush.
		m.input.Blur()
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// s8StartupGate forces the renderer's ticker to flush before the first View.
// It models a delayed initializer without relying on scheduling or elapsed time.
type s8StartupGate struct {
	s8Model
	ctx     context.Context
	release <-chan struct{}
}

func (m s8StartupGate) Init() tea.Cmd {
	select {
	case <-m.release:
	case <-m.ctx.Done():
	}
	return m.s8Model.Init()
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
	var normal, startup []byte
	t.Run("normal", func(t *testing.T) { normal = testS8(t, false) })
	t.Run("startup-flush", func(t *testing.T) { startup = testS8(t, true) })
	// Publish the normal recording only after both scenarios ran and passed.
	// A failed or filtered-out regression must not produce passing evidence.
	if t.Failed() || len(normal) == 0 || len(startup) == 0 || *evidenceOutput == "" {
		return
	}
	if err := os.WriteFile(*evidenceOutput, append(normal, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func testS8(t *testing.T, delayInit bool) []byte {
	t.Helper()
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
	model := s8Model{input: input, sealed: sealed}
	var initial tea.Model = model
	release := make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	if delayInit {
		initial = s8StartupGate{s8Model: model, ctx: ctx, release: release}
	}
	h, err := term.Start(ctx, initial, 20, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	// Release Init before joining the program even if an assertion fails.
	defer open()
	if delayInit {
		_, err := h.Output.Wait(ctx, func(s term.Snapshot) bool {
			return s.Bytes > 0 && !s.AltScreen && s.CursorHidden
		})
		if err != nil {
			t.Fatal(err)
		}
		open()
	}
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
		t.Fatalf("quit did not restore the terminal: alt=%t hidden=%t bytes=%d writes=%d output=%q", restored.AltScreen, restored.CursorHidden, restored.Bytes, restored.Writes, h.Output.Bytes())
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
	return data
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
