// SPDX-License-Identifier: Apache-2.0

// Package lease makes the lifetime of external work explicit. A lease tracks
// activity; it does not grant permission to perform the work.
package lease

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Class distinguishes work that blocks idle from work reported for resumption.
type Class string

const (
	Hard     Class = "hard"     // Effects, Node commands, worker turns, migrations and restores.
	Live     Class = "live"     // Streaming model requests and read-only tools.
	Reported Class = "reported" // Queued input, timers, background runs, derivation and backups.
)

type Activity struct {
	Class  Class
	Reason string
}

func (a Activity) Validate() error {
	switch a.Class {
	case Hard, Live, Reported:
	default:
		return errors.New("unknown activity class")
	}
	if strings.TrimSpace(a.Reason) == "" {
		return errors.New("activity requires a reason")
	}
	return nil
}

type Leaser interface {
	Acquire(context.Context, Activity) (Lease, error)
}

var (
	ErrInvalid  = errors.New("external call requires a valid lease")
	ErrReleased = errors.New("lease has been released")
)

// Lease is a handle to registered activity. Copies share one lifetime. The zero
// value cannot run work. Cancellation never releases a lease: its owner releases
// it when the work is actually finished.
type Lease struct{ state *state }

type state struct {
	mu       sync.Mutex
	released bool
	running  int
	finish   func()
}

// New is for Leaser implementations: register the activity first, then supply
// the callback that removes it. A nil callback is a programming error.
func New(release func()) Lease {
	if release == nil {
		panic("lease: nil release callback")
	}
	return Lease{state: &state{finish: release}}
}

// Release rejects new calls immediately. Removal from the registry waits until
// every call already inside Do has returned. Releasing a copy or a zero lease is
// safe, and the removal callback runs exactly once.
func (l Lease) Release() {
	if l.state == nil {
		return
	}
	s := l.state
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return
	}
	s.released = true
	done := s.running == 0
	s.mu.Unlock()
	if done {
		s.finish()
	}
}

// Do is the external-call boundary for kit adapters. Its required Lease argument
// makes omission a compile error; invalid and released handles fail before the
// callback runs. The callback receives the original context and owns cancellation
// of its external operation. Do never retries or ends work on a timeout.
func Do(ctx context.Context, l Lease, call func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.state == nil {
		return ErrInvalid
	}
	s := l.state
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return ErrReleased
	}
	s.running++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running--
		done := s.released && s.running == 0
		s.mu.Unlock()
		if done {
			s.finish()
		}
	}()
	return call(ctx)
}
