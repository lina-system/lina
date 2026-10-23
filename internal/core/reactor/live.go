// SPDX-License-Identifier: Apache-2.0
package reactor

import (
	"context"
)

// SessionOwner ends every live worker session outside a keep set.
type SessionOwner interface {
	EndUnlisted(ctx context.Context, keep map[string]bool) error
}

// LiveSessions ends worker sessions whose steps have landed or been closed.
// Keep names the handles that may stay live: steps in progress or under
// pull-request operation. It is read from committed state, never from a worker
// claim, so two reaps over the same ledger end the same sessions.
type LiveSessions struct {
	Sessions SessionOwner
	Keep     func(context.Context) (map[string]bool, error)
}

// Reap ends every live session outside keep. An end failure keeps the session
// for the next event or sweep; it never reports a process as ended that was not.
func (l LiveSessions) Reap(ctx context.Context, keep map[string]bool) error {
	if l.Sessions == nil {
		return nil
	}
	return l.Sessions.EndUnlisted(ctx, keep)
}

// ReapCurrent reads the current keep set before ending sessions. A missing
// source ends nothing rather than treating an unavailable read as an empty one.
func (l LiveSessions) ReapCurrent(ctx context.Context) error {
	if l.Sessions == nil || l.Keep == nil {
		return nil
	}
	keep, err := l.Keep(ctx)
	if err != nil {
		return err
	}
	return l.Reap(ctx, keep)
}
