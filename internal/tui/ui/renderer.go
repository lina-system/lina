// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// terminalRenderer lets the existing connection owner keep bounded, ordered
// state updates while Bubble Tea owns cell diffing, cursor and terminal modes.
// Input has one separate cancellable decoder; the renderer never reads stdin.
type terminalRenderer struct {
	last    tea.View
	shown   bool
	program *tea.Program
	done    chan struct{}
	err     error
	once    sync.Once
}
type renderedView struct{ view tea.View }

func (m renderedView) Init() tea.Cmd { return nil }
func (m renderedView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if view, ok := msg.(tea.View); ok {
		m.view = view
	}
	return m, nil
}
func (m renderedView) View() tea.View { return m.view }

func startTerminalRenderer(ctx context.Context, output io.Writer, width, height int) *terminalRenderer {
	r := &terminalRenderer{done: make(chan struct{})}
	r.program = tea.NewProgram(renderedView{}, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(output), tea.WithWindowSize(width, height), tea.WithoutSignalHandler(), tea.WithoutCatchPanics())
	go func() {
		defer close(r.done)
		_, r.err = r.program.Run()
		if ctx.Err() != nil {
			r.err = nil
		}
	}()
	return r
}
func (r *terminalRenderer) show(view tea.View) error {
	if r.shown && reflect.DeepEqual(r.last, view) {
		return nil
	}
	select {
	case <-r.done:
		if r.err == nil {
			return io.ErrClosedPipe
		}
		return r.err
	default:
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
	translated := bytes.ReplaceAll(data, []byte{'\n'}, []byte{'\r', '\n'})
	_, err := w.output.Write(translated)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}
