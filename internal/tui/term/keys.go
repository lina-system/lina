// SPDX-License-Identifier: Apache-2.0

// Package term adapts terminal input for the UI. It never owns Core state.
package term

import "time"

type KeyClass uint8

const (
	Toggle KeyClass = iota
	Edit
	Approve
)

type KeyEvent uint8

const (
	Press KeyEvent = iota
	Repeat
	Release
)

// Key carries the terminal's event type, not a guess based on arrival time.
// Events is false for legacy terminals, which report presses only.
type Key struct {
	Name   string
	Event  KeyEvent
	Events bool
}

// ApprovalDeadTime is the provisional T-10 barrier for terminals without
// release events. The caller supplies time, so admission is deterministic.
const ApprovalDeadTime = 250 * time.Millisecond

type Gate struct {
	cardChanged time.Time
	hasCard     bool
}

// CardChanged also runs when a card's choices or group revision changes.
func (g *Gate) CardChanged(now time.Time) { g.cardChanged, g.hasCard = now, true }

// Admit is the single gate for every key before an action is dispatched.
func (g *Gate) Admit(key Key, class KeyClass, now time.Time) bool {
	switch class {
	case Toggle, Edit, Approve:
	default:
		return false
	}
	// Explicit event types remain authoritative before capability negotiation.
	// Events controls only whether a press needs the legacy card barrier.
	switch key.Event {
	case Press:
		if key.Events {
			return true
		}
	case Repeat:
		return class == Edit
	case Release:
		return false
	default:
		return false
	}
	return class != Approve || !g.hasCard || now.Sub(g.cardChanged) >= ApprovalDeadTime
}
