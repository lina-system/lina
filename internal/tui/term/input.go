// SPDX-License-Identifier: Apache-2.0

package term

import (
	"context"
	"errors"
	"os"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ReadKeys decodes terminal events, excluding paste and terminal replies from
// key actions. Returning false from visit stops reading. Cancellation joins the
// decoder and closes only its own resources; input remains the caller's file.
func ReadKeys(ctx context.Context, input *os.File, visit func(Key) bool) (result error) {
	if ctx.Err() != nil {
		return nil
	}
	reader, err := uv.NewCancelReader(input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { reader.Cancel(); close(interrupted) })
	defer func() {
		cancel()
		if !stop() {
			<-interrupted
		}
		result = errors.Join(result, reader.Close())
	}()
	events := make(chan uv.Event)
	done := make(chan error, 1)
	go func() {
		err := uv.NewTerminalReader(reader, os.Getenv("TERM")).StreamEvents(ctx, events)
		close(events)
		done <- err
	}()
	enhanced := false
	stopping := false
	for event := range events {
		// Drain after cancellation: the decoder may flush buffered events while
		// joining its reader, but those events must dispatch no further action.
		if stopping || ctx.Err() != nil {
			continue
		}
		var key Key
		switch event := event.(type) {
		case uv.KeyboardEnhancementsEvent:
			enhanced = event.Flags&ansi.KittyReportEventTypes != 0
			continue
		case uv.KeyPressEvent:
			key = Key{Name: event.Keystroke(), Events: enhanced, Event: Press}
			if event.IsRepeat {
				key.Event, key.Events = Repeat, true
			}
		case uv.KeyReleaseEvent:
			key = Key{Name: event.Keystroke(), Events: true, Event: Release}
		default:
			continue
		}
		if !visit(key) {
			stopping = true
			cancel()
		}
	}
	return <-done
}
