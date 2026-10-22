// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/term"
)

type terminalInput interface {
	io.Reader
	Fd() uintptr
}

// The cancellation library needs a file-shaped reader to interrupt blocked
// reads. Supply its unused methods without taking ownership of the input or
// requiring wrappers to expose Write, Close and Name. Reads keep the wrapper.
type borrowedTerminalInput struct{ terminalInput }

func (borrowedTerminalInput) Write([]byte) (int, error) { return 0, os.ErrPermission }
func (borrowedTerminalInput) Close() error              { return nil }
func (borrowedTerminalInput) Name() string              { return "terminal input" }

// One decoded reader owns the terminal until cancellation; it is joined before
// Run restores raw mode. Paste is a single edit, never interpreted as keys.
func readConversationInput(parent context.Context, input terminalInput, visit func(tea.Msg) bool) (result error) {
	reader, err := uv.NewCancelReader(borrowedTerminalInput{input})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { reader.Cancel(); close(interrupted) })
	defer func() {
		cancel()
		if !stop() {
			<-interrupted
		}
		result = errors.Join(result, reader.Close())
	}()
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	resized := make(chan struct{})
	go func() {
		defer close(resized)
		for {
			select {
			case <-ctx.Done():
				return
			case <-resizes:
				if w, h, err := term.GetSize(input.Fd()); err == nil {
					if !visit(tea.WindowSizeMsg{Width: w, Height: h}) {
						return
					}
				}
			}
		}
	}()
	defer func() { signal.Stop(resizes); cancel(); <-resized }()
	events := make(chan uv.Event)
	done := make(chan error, 1)
	go func() {
		err := uv.NewTerminalReader(reader, os.Getenv("TERM")).StreamEvents(ctx, events)
		close(events)
		done <- err
	}()
	if w, h, err := term.GetSize(input.Fd()); err == nil {
		visit(tea.WindowSizeMsg{Width: w, Height: h})
	}
	for event := range events {
		if ctx.Err() != nil {
			continue
		}
		var msg tea.Msg
		switch e := event.(type) {
		case uv.KeyPressEvent:
			msg = tea.KeyPressMsg(e)
		case uv.KeyReleaseEvent:
			msg = tea.KeyReleaseMsg(e)
		case uv.MouseWheelEvent:
			msg = tea.MouseWheelMsg(e)
		case uv.FocusEvent:
			msg = tea.FocusMsg{}
		case uv.BlurEvent:
			msg = tea.BlurMsg{}
		case uv.PasteEvent:
			msg = tea.PasteMsg{Content: e.Content}
		case uv.KeyboardEnhancementsEvent:
			msg = tea.KeyboardEnhancementsMsg(e)
		default:
			continue
		}
		if !visit(msg) {
			cancel()
		}
	}
	return <-done
}
