// SPDX-License-Identifier: Apache-2.0

package term

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// Harness runs the real Bubble Tea renderer without opening a TTY or reading
// the caller's environment. Messages enter through Send, output through Output.
// Callers must Close it, including when an assertion or a scenario fails.
type Harness struct {
	Output  *Recorder
	program *tea.Program
	cancel  context.CancelFunc
	done    chan struct{}
	model   tea.Model
	err     error
}

func Start(ctx context.Context, model tea.Model, width, height int) (*Harness, error) {
	if model == nil {
		return nil, errors.New("recording model is required")
	}
	output, err := NewRecorder(width, height)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &Harness{Output: output, cancel: cancel, done: make(chan struct{})}
	h.program = tea.NewProgram(model,
		tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(output),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}),
		tea.WithColorProfile(colorprofile.TrueColor), tea.WithWindowSize(width, height),
		tea.WithoutSignalHandler(), tea.WithoutCatchPanics(),
	)
	go func() {
		defer close(h.done)
		defer output.Close()
		h.model, h.err = h.program.Run()
	}()
	return h, nil
}

func (h *Harness) Send(message tea.Msg) { h.program.Send(message) }

// Wait joins Run and its terminal teardown. Context cancellation supplied to
// Start also stops the program, so failed scenarios need no sleep or polling.
func (h *Harness) Wait() (tea.Model, error) {
	<-h.done
	return h.model, h.err
}

// Close cancels and joins the run. It is safe to call again after Wait or Close.
func (h *Harness) Close() {
	h.cancel()
	<-h.done
}
