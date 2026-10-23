// SPDX-License-Identifier: Apache-2.0

// Package macos translates trusted session notifications without collecting
// screen contents or preventing sleep. A native Source is supplied by the Node
// host; the platform adapter can be tested without a macOS GUI.
package macos

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/node/seat"
)

type Kind string

const (
	PhysicalInput   Kind = "physical_input"
	SyntheticInput  Kind = "synthetic_input"
	SessionLocked   Kind = "session_locked"
	SessionUnlocked Kind = "session_unlocked"
	SystemResumed   Kind = "system_resumed"
)

type Notification struct {
	Kind    Kind
	Session string
}

// Source must distinguish physical input from injected input and stop Next on
// context cancellation. Session ids and event origin come from the OS, not work.
type Source interface {
	Next(context.Context) (Notification, error)
}

// Forward fails closed on loss of monitoring, unknown/malformed notifications,
// cancellation or a handler error. It never turns an unlock into a handover.
func Forward(ctx context.Context, source Source, handle func(seat.Event) error) (err error) {
	defer func() { err = errors.Join(err, handle(seat.Event{Kind: seat.EventsLost})) }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := source.Next(ctx)
		if err != nil {
			return err
		}
		e := seat.Event{Session: n.Session}
		switch n.Kind {
		case PhysicalInput:
			e.Kind = seat.HumanInput
		case SyntheticInput:
			if n.Session == "" {
				return seat.ErrEvent
			}
			continue
		case SessionLocked:
			e.Kind = seat.Locked
		case SessionUnlocked:
			e.Kind = seat.Unlocked
		case SystemResumed:
			e.Kind = seat.Resumed
		default:
			return seat.ErrEvent
		}
		if n.Kind == SystemResumed && n.Session != "" || n.Kind != SystemResumed && n.Session == "" {
			return seat.ErrEvent
		}
		if err := handle(e); err != nil {
			return err
		}
	}
}
