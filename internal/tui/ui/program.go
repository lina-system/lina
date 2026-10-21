// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The shell renders the generic text stream of these capabilities. Feature
// actions (approvals, editing results, sending messages) are not advertised.
var shellCapabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}, {Name: "running", Version: "1.0"}, {Name: "progress", Version: "1.0"}, {Name: "waiting", Version: "1.0"}}

type clientPeer struct{ client *clientapi.Client }

func (p clientPeer) Welcome() protocol.Welcome { return p.client.Welcome }
func (p clientPeer) Receive(ctx context.Context) (protocol.Envelope, error) {
	return p.client.Receive(ctx)
}
func (p clientPeer) Close() error { return p.client.Close() }
func (p clientPeer) Subscribe(ctx context.Context, request protocol.Subscribe) (string, error) {
	return p.client.Subscribe(ctx, request)
}

type programEvent struct {
	message      tea.Msg
	acknowledged chan error
}

func runClientShell(parent context.Context, socket, version string, input io.Reader, display *display, terminal bool) (result error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(context.Canceled)
	model := newShell(environmentOptions(os.Getenv), time.Now)
	if display.interactive {
		model.styles = display.styles
		model.size.Width = display.styles.options.Width
	}
	events := make(chan programEvent, 64)
	done := make(chan error, 1)
	go func() {
		done <- connectionLoop(ctx, func(ctx context.Context) (connectionPeer, error) {
			client, err := clientapi.DialWithCapabilities(ctx, socket, version, shellCapabilities)
			if err != nil {
				return nil, err
			}
			return clientPeer{client}, nil
		}, waitRetry, func(message tea.Msg) error {
			event := programEvent{message: message, acknowledged: make(chan error, 1)}
			select {
			case events <- event:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case err := <-event.acknowledged:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	joined := false
	defer func() {
		cancel(context.Canceled)
		if !joined {
			<-done
		}
	}()
	var peer connectionPeer
	var stopInput func() error
	defer func() {
		if stopInput != nil {
			result = errors.Join(result, stopInput())
		}
	}()
	inputStarted := terminal
	var handle func(tea.Msg) error
	handle = func(message tea.Msg) error {
		if attached, ok := message.(shellPeer); ok {
			peer = attached.Peer
			w := peer.Welcome()
			envelope := protocol.Envelope{Protocol: protocol.Protocol{Name: "client", Version: "1.0"}, Kind: protocol.KindWelcome, MessageID: "local-welcome", AttemptID: "local-welcome", Sender: protocol.Sender{Party: protocol.PartyCore, ID: w.MainID}, Epoch: w.Epoch, SentAt: time.Now().UTC().Format(time.RFC3339Nano), EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: w}
			data, err := json.Marshal(envelope)
			if err != nil {
				return err
			}
			_, cmd := model.Update(shellData{Generation: attached.Generation, Message: state.Frame{Data: data}})
			if err := runShellCommand(cmd, handle); err != nil {
				return err
			}
			if len(w.Capabilities) > 0 {
				if err := handle(shellSubscription{Generation: attached.Generation, Request: protocol.Subscribe{Topic: stringPointer("core")}}); err != nil {
					return err
				}
				for _, cap := range w.Capabilities {
					switch cap.Name {
					case "waiting", "progress", "running":
						if err := handle(shellSubscription{Generation: attached.Generation, Request: protocol.Subscribe{Topic: stringPointer(cap.Name)}}); err != nil {
							return err
						}
					}
				}
			}
			return nil
		}
		if request, ok := message.(shellSubscription); ok {
			if request.Generation != model.generation {
				return nil
			}
			if client, ok := peer.(interface {
				Subscribe(context.Context, protocol.Subscribe) (string, error)
			}); ok {
				_, err := client.Subscribe(ctx, request.Request)
				return err
			}
			return nil
		}
		_, command := model.Update(message)
		if err := runShellCommand(command, handle); err != nil {
			return err
		}
		if !inputStarted && model.status.State == Live {
			inputStarted = true
			if file, ok := input.(*os.File); ok {
				inputContext, stop, err := watchTerminalInput(ctx, file)
				if err != nil {
					return err
				}
				forwarded := make(chan struct{})
				stopInput = func() error { err := stop(); <-forwarded; return err }
				go func() { defer close(forwarded); <-inputContext.Done(); cancel(context.Cause(inputContext)) }()
			} else {
				go func() {
					if err := readQuit(input); err != nil {
						cancel(err)
					} else {
						cancel(errQuit)
					}
				}()
			}
		}
		return display.showShell(model)
	}
	for {
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if errors.Is(cause, errQuit) || errors.Is(cause, context.Canceled) {
				return display.show(Quit)
			}
			return cause
		case err := <-done:
			joined = true
			if ctx.Err() != nil {
				if cause := context.Cause(ctx); errors.Is(cause, errQuit) || errors.Is(cause, context.Canceled) {
					return display.show(Quit)
				}
				return context.Cause(ctx)
			}
			if err != nil && display.interactive {
				return &presentedError{err}
			}
			return err
		case event := <-events:
			err := handle(event.message)
			event.acknowledged <- err
			if err != nil {
				return err
			}
		}
	}
}

func stringPointer(s string) *string { return &s }
func runShellCommand(command tea.Cmd, handle func(tea.Msg) error) error {
	if command == nil {
		return nil
	}
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, command := range batch {
			if err := runShellCommand(command, handle); err != nil {
				return err
			}
		}
		return nil
	}
	return handle(message)
}

func (d *display) showShell(model *shellModel) error {
	if d.interactive && d.styles.options.Profile != Monochrome {
		frame := model.View().Content + "\n"
		if frame == d.frame {
			return nil
		}
		output := frame
		if d.rows > 0 {
			output = ansi.CursorUp(d.rows) + ansi.EraseScreenBelow + output
		}
		if err := d.write(output); err != nil {
			return err
		}
		d.frame = frame
		d.rows = strings.Count(frame, "\n")
		d.last, d.shown = model.status, true
		return nil
	}
	// Notices are local, code-driven status, never ledger items or peer detail.
	for d.notices < len(model.content.Notices) {
		notice := model.content.Notices[d.notices]
		if d.interactive {
			if err := d.write(design.Text(model.styles.options.Language, notice.Code) + "\n"); err != nil {
				return err
			}
		}
		d.notices++
	}
	if err := d.showStatus(model.status); err != nil {
		return err
	}
	if !d.interactive || len(model.content.Blocks) == 0 {
		return nil
	}
	frame := model.View().Content
	if frame == d.frame {
		return nil
	}
	if err := d.write(frame + "\n"); err != nil {
		return err
	}
	d.frame = frame
	return nil
}
