// SPDX-License-Identifier: Apache-2.0

package term_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/term"
)

func TestRecordingParsesSplitWritesAndCopiesSnapshots(t *testing.T) {
	r, err := term.NewRecorder(20, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Split both ANSI and UTF-8; assertions concern cells, widths and cursor.
	data := []byte("\x1b[?1049h\x1b[2J\x1b[H한A\x1b[2;5H\x1b[?25h")
	for _, b := range data {
		if _, err := r.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	s := r.Snapshot()
	if !s.AltScreen || s.CursorX != 4 || s.CursorY != 1 || s.CursorHidden || s.Cells[0][0].Content != "한" || s.Cells[0][0].Width != 2 || s.Cells[0][2].Content != "A" || s.Bytes != len(data) || s.Writes != len(data) {
		t.Fatalf("snapshot: %+v", s)
	}
	s.Cells[0][0].Content = "changed"
	if r.Snapshot().Cells[0][0].Content != "한" {
		t.Fatal("snapshot aliases emulator")
	}
	raw := r.Bytes()
	raw[0] = 0
	if r.Bytes()[0] != 27 {
		t.Fatal("bytes alias recording")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after close: %v", err)
	}
	if _, err := r.Wait(context.Background(), func(term.Snapshot) bool { return false }); !errors.Is(err, io.EOF) {
		t.Fatalf("wait after close: %v", err)
	}
}

func TestWaitObservesWritesAndCancellation(t *testing.T) {
	r, _ := term.NewRecorder(10, 2)
	defer r.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.Wait(ctx, func(s term.Snapshot) bool { return s.Cells[0][0].Content == "7" })
		done <- err
	}()
	if _, err := r.Write([]byte("7")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := r.Wait(ctx, func(term.Snapshot) bool { return false }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait: %v", err)
	}
}

type fixtureModel struct{ n int }

func (m fixtureModel) Init() tea.Cmd { return nil }
func (m fixtureModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.Code == 'q' {
		return m, tea.Quit
	}
	if n, ok := msg.(int); ok {
		m.n = n
	}
	return m, nil
}
func (m fixtureModel) View() tea.View {
	v := tea.NewView(string(rune('0'+m.n)) + "한")
	v.AltScreen = true
	v.Cursor = tea.NewCursor(3, 0)
	return v
}

func TestHarnessRunsUpdatesQuitsAndRestoresTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h, err := term.Start(ctx, fixtureModel{}, 20, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, n := range []int{0, 1, 2} {
		h.Send(n)
		s, err := h.Output.Wait(ctx, func(s term.Snapshot) bool {
			return s.AltScreen && s.Cells[0][0].Content == string(rune('0'+n)) && s.CursorX == 3 && s.CursorY == 0 && !s.CursorHidden
		})
		if err != nil {
			t.Fatal(err)
		}
		if s.Cells[0][1].Width != 2 {
			t.Fatalf("wide cell: %+v", s.Cells[0][1])
		}
	}
	h.Send(tea.KeyPressMsg{Code: 'q'})
	model, err := h.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if model.(fixtureModel).n != 2 {
		t.Fatal("lost update")
	}
	if s := h.Output.Snapshot(); s.AltScreen || s.CursorHidden || s.Bytes == 0 {
		t.Fatalf("terminal not restored: %+v", s)
	}
}

func TestHarnessJoinsOnCancellationAndEarlyExit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	h, err := term.Start(ctx, fixtureModel{}, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := h.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	h.Close()
	h.Close()
	if _, err := term.Start(t.Context(), nil, 10, 2); err == nil {
		t.Fatal("nil model accepted")
	}
	if _, err := term.NewRecorder(0, 2); err == nil {
		t.Fatal("invalid size accepted")
	}
}
