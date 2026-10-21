// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestShellConnectionStatesRetainWindow(t *testing.T) {
	m := newShell(Options{Width: 80, Profile: Monochrome}, func() time.Time { return time.Time{} })
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "42", Revocation: "1", Topic: "conversation:example", Cursor: 1, Items: []state.Block{{ID: "item", Text: "한글 history", Revision: 1, Done: true}}}})
	m.Update(shellData{Generation: 1, Message: state.EditDraft{Draft: state.Draft{Text: "draft"}}})
	before := m.content
	for _, connection := range []Connection{Live, Resyncing, Retrying, Recovering, Refused, Stopped, Failed} {
		m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: connection}})
		if m.status.State != connection || !reflect.DeepEqual(m.content, before) {
			t.Fatalf("connection %s changed retained content", connection.Code())
		}
		if m.mutationsAllowed() != (connection == Live) {
			t.Fatalf("mutation admission for %s", connection.Code())
		}
		if connection.active() && !strings.Contains(ansi.Strip(m.View().Content), "한글 history") {
			t.Fatalf("connection %s hid the readable history", connection.Code())
		}
	}
	m.Update(shellBegin{Generation: 2})
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	m.Update(shellData{Generation: 1, Message: state.EditDraft{Draft: state.Draft{Text: "stale"}}})
	if m.status.State != Retrying || !reflect.DeepEqual(m.content, before) {
		t.Fatal("stale connection changed window")
	}
}

func TestShellAbsentScreenKeepsHistoryOffScreen(t *testing.T) {
	m := newShell(Options{Width: 80, Profile: Monochrome}, time.Now)
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "42", Revocation: "1", Topic: "conversation:example", Cursor: 1, Items: []state.Block{{ID: "item", Text: "RETAINED-HISTORY", Revision: 1, Done: true}}}})
	for _, connection := range []Connection{Stopped, Refused, Failed} {
		m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: connection, Cause: protocol.ErrorUnavailable}})
		view := ansi.Strip(m.View().Content)
		if strings.Contains(view, "RETAINED-HISTORY") || strings.Contains(view, design.Text(design.English, "shell.composer")) {
			t.Fatal("connection failure displayed a conversation surface")
		}
		if !strings.Contains(view, design.Text(design.English, connection.Code())) || !strings.Contains(view, string(protocol.ErrorUnavailable)) {
			t.Fatal("failure cause absent")
		}
		if len(m.content.Blocks) != 1 {
			t.Fatal("hidden history was discarded")
		}
	}
}

func TestShellProgramRetainsReadableHistoryDuringRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h, err := term.Start(ctx, newShell(Options{Width: 80, Profile: Monochrome}, time.Now), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.Send(shellData{Generation: 1, Message: state.Snapshot{Epoch: "42", Revocation: "1", Topic: "conversation:example", Cursor: 1, Items: []state.Block{{ID: "item", Text: "HISTORY-42", Revision: 1, Done: true}}}})
	for _, connection := range []Connection{Live, Retrying, Resyncing, Recovering, Live} {
		h.Send(shellConnection{Generation: 1, Status: ConnectionStatus{State: connection}})
		_, err := h.Output.Wait(ctx, func(snapshot term.Snapshot) bool {
			var cells strings.Builder
			for _, row := range snapshot.Cells {
				for _, cell := range row {
					cells.WriteString(cell.Content)
				}
				cells.WriteByte('\n')
			}
			return strings.Contains(cells.String(), "HISTORY-42") && strings.Contains(cells.String(), design.Text(design.English, connection.Code()))
		})
		if err != nil {
			t.Fatalf("state %s: %v", connection.Code(), err)
		}
	}
	h.Send(tea.KeyPressMsg{Code: 'q'})
	final, err := h.Wait()
	if err != nil || final.(*shellModel).status.State != Quit {
		t.Fatalf("quit: %v", err)
	}
	if snapshot := h.Output.Snapshot(); snapshot.AltScreen || snapshot.CursorHidden {
		t.Fatal("terminal modes not restored")
	}
}

func TestShellKeysPassAdmission(t *testing.T) {
	m := newShell(Options{Width: 80}, func() time.Time { return time.Time{} })
	for _, msg := range []tea.Msg{tea.KeyReleaseMsg{Code: 'q'}, tea.KeyPressMsg{Code: 'q', IsRepeat: true}, tea.PasteMsg{Content: "q"}} {
		_, command := m.Update(msg)
		if command != nil || m.status.State == Quit {
			t.Fatal("release, repeat or paste dispatched quit")
		}
	}
	_, command := m.Update(tea.KeyPressMsg{Code: 'q'})
	if command == nil || m.status.State != Quit {
		t.Fatal("press did not quit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("wrong quit effect")
	}
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	if m.status.State != Quit {
		t.Fatal("late connection revived a quit client")
	}
	view := m.View()
	if !view.KeyboardEnhancements.ReportEventTypes || view.KeyboardEnhancements.ReportAllKeysAsEscapeCodes {
		t.Fatal("unsafe keyboard flags")
	}
}

func TestShellControlCClearsDraftBeforeQuit(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	m.Update(shellData{Generation: 1, Message: state.EditDraft{Draft: state.Draft{Text: "pending", Attachments: []string{"asset"}}}})
	_, command := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command != nil || m.content.Draft.Text != "" || len(m.content.Draft.Attachments) != 0 {
		t.Fatal("control-c did not clear draft")
	}
	_, command = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command == nil || m.status.State != Quit {
		t.Fatal("empty control-c did not quit")
	}
}

func TestShellEpochNoticeStaysOutsideHistory(t *testing.T) {
	m := newShell(Options{Width: 80, Profile: Monochrome}, time.Now)
	snapshot := state.Snapshot{Epoch: "42", Revocation: "1", Topic: "conversation:example", Cursor: 1, Items: []state.Block{{ID: "item", Text: "HISTORY", Revision: 1, Done: true}}}
	m.Update(shellData{Generation: 1, Message: snapshot})
	snapshot.Epoch = "43"
	m.Update(shellData{Generation: 1, Message: snapshot})
	m.Update(shellData{Generation: 1, Message: snapshot})
	if len(m.content.Notices) != 1 || m.content.Notices[0].Code != "epoch_changed" || len(m.content.Blocks) != 1 {
		t.Fatal("epoch notice duplicated or entered history")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), design.Text(design.English, "epoch_changed")) {
		t.Fatal("epoch notice is not visible")
	}
	snapshot.Cursor++
	snapshot.Items = nil
	m.Update(shellData{Generation: 1, Message: snapshot})
	if len(m.content.Blocks) != 0 || !strings.Contains(ansi.Strip(m.View().Content), design.Text(design.English, "epoch_changed")) {
		t.Fatal("empty history hid the local epoch notice")
	}
}

func TestShellViewFitsTerminalAndKeepsStatus(t *testing.T) {
	m := newShell(Options{Width: 80, Profile: Monochrome, Language: design.Korean}, time.Now)
	for _, size := range []tea.WindowSizeMsg{{Width: 1, Height: 1}, {Width: 32, Height: 5}, {Width: 80, Height: 24}, {Width: 0, Height: 0}} {
		m.Update(size)
		view := m.View()
		content := view.Content
		if size.Width == 0 || size.Height == 0 {
			if content != "" {
				t.Fatal("zero terminal has content")
			}
			continue
		}
		lines := strings.Split(content, "\n")
		if len(lines) > size.Height {
			t.Fatalf("height %d overflow", size.Height)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("width %d overflow", size.Width)
			}
		}
	}
}
