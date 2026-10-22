// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// terminalRenderer lets the existing connection owner keep bounded, ordered
// state updates while Bubble Tea owns cell diffing, cursor and terminal modes.
// Input has one separate cancellable decoder; the renderer never reads stdin.
type terminalRenderer struct {
	expectedSize atomic.Pointer[tea.WindowSizeMsg]
	last         tea.View
	size         tea.WindowSizeMsg
	shown        bool
	program      *tea.Program
	done         chan struct{}
	err          error
	once         sync.Once
}
type renderedView struct{ view tea.View }

// The connection owner's input decoder is the sole size source. Bubble Tea
// sends its startup size asynchronously; that stale message must not undo a
// newer size already forwarded with a view.
type rendererWindowSize tea.WindowSizeMsg

func rendererMessages(_ tea.Model, message tea.Msg) tea.Msg {
	switch message := message.(type) {
	case rendererWindowSize:
		return tea.WindowSizeMsg(message)
	case tea.WindowSizeMsg:
		return nil
	}
	return message
}

func (m renderedView) Init() tea.Cmd { return nil }
func (m renderedView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if view, ok := msg.(tea.View); ok {
		m.view = view
	}
	return m, nil
}
func (m renderedView) View() tea.View { return m.view }

func startTerminalRenderer(ctx context.Context, output io.Writer, width, height int) *terminalRenderer {
	rendererContext, cancel := context.WithCancel(ctx)
	r := &terminalRenderer{done: make(chan struct{}), size: tea.WindowSizeMsg{Width: width, Height: height}}
	initial := r.size
	r.expectedSize.Store(&initial)
	r.program = tea.NewProgram(renderedView{}, tea.WithFilter(func(model tea.Model, message tea.Msg) tea.Msg {
		return r.filterSize(model, rendererMessages(model, message))
	}), tea.WithContext(rendererContext), tea.WithInput(nil), tea.WithOutput(output), tea.WithWindowSize(width, height), tea.WithoutSignalHandler(), tea.WithoutCatchPanics())
	go func() {
		defer close(r.done)
		defer cancel()
		// Bubble Tea's mouse modes request drag/all motion. Own DEC 1000
		// (buttons and wheels) instead; the input adapter dispatches only wheels.
		// Run and cleanup share this output owner, including cancellation/errors.
		defer func() {
			_, err := io.WriteString(output, ansi.ResetModeMouseNormal+ansi.ResetModeMouseExtSgr)
			r.err = errors.Join(r.err, err)
		}()
		if _, err := io.WriteString(output, ansi.SetModeMouseNormal+ansi.SetModeMouseExtSgr); err != nil {
			r.err = err
			return
		}
		_, r.err = r.program.Run()
		if ctx.Err() != nil {
			r.err = nil
		}
	}()
	return r
}

// The connection owner is the sole source of dimensions. Bubble Tea can emit
// its asynchronous startup size after a newer frame was sent; reject that
// stale message before it resizes the cell buffer. Non-size messages pass.
func (r *terminalRenderer) filterSize(_ tea.Model, message tea.Msg) tea.Msg {
	if size, ok := message.(tea.WindowSizeMsg); ok {
		if expected := r.expectedSize.Load(); expected != nil && size != *expected {
			return nil
		}
	}
	return message
}

func (r *terminalRenderer) show(view tea.View, size tea.WindowSizeMsg) error {
	if r.shown && r.size == size && reflect.DeepEqual(r.last, view) {
		return nil
	}
	select {
	case <-r.done:
		if r.err == nil {
			return io.ErrClosedPipe
		}
		return r.err
	default:
		if r.size != size {
			// Output is wrapped for raw mode, so Bubble Tea cannot query its
			// terminal dimensions. Resize its cell buffer before the new view.
			r.expectedSize.Store(&size)
			r.program.Send(rendererWindowSize(size))
			r.size = size
		}
		r.program.Send(view)
		r.last = view
		r.shown = true
		return nil
	}
}
func (r *terminalRenderer) close() error {
	r.once.Do(func() { r.program.Quit(); <-r.done })
	return r.err
}

// Run owns raw mode, including disabled output postprocessing. Bubble Tea's
// renderer uses LF between rows; supply the CR that a cooked TTY would add.
type rawTerminalWriter struct{ output io.Writer }

func (w rawTerminalWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	translated := bytes.ReplaceAll(data, []byte{'\n'}, []byte{'\r', '\n'})
	// A queued frame may flush after the physical terminal has already grown.
	// The cell renderer's scroll optimization restores margins to its cached
	// height, which can leave the terminal confined to the old, shorter screen.
	// Finish each write with the terminal's own full-height margins. Preserve
	// the hardware cursor because DECSTBM homes it; the next diff still relies
	// on the renderer's cursor position. This emits nothing on unchanged frames.
	translated = append(translated, ansi.SaveCursor+ansi.SetTopBottomMargins(0, 0)+ansi.RestoreCursor...)
	_, err := w.output.Write(translated)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func (r *terminalRenderer) raw(text string) error {
	select {
	case <-r.done:
		if r.err != nil {
			return r.err
		}
		return io.ErrClosedPipe
	default:
	}
	r.program.Send(tea.Raw(text)())
	return nil
}
