// SPDX-License-Identifier: Apache-2.0

// Package rpc provides shared client stream and transport mechanics.
package rpc

import (
	"errors"
	"strconv"
)

var (
	ErrInvalidCursor  = errors.New("invalid cursor")
	ErrStaleCursor    = errors.New("stale cursor")
	ErrResyncRequired = errors.New("resync_required")
)

// Cursor identifies the last applied frame of one topic. Epoch is a canonical
// decimal uint64, never a floating-point value or a lexically ordered version.
type Cursor struct {
	Epoch string `json:"epoch"`
	Seq   uint64 `json:"seq"`
}

func (c Cursor) Validate() error {
	n, err := strconv.ParseUint(c.Epoch, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != c.Epoch {
		return ErrInvalidCursor
	}
	return nil
}

func epochNumber(epoch string) uint64 { n, _ := strconv.ParseUint(epoch, 10, 64); return n }

// Sequence is the receive state for one topic. Apply content only when Accept
// returns true; duplicates leave content untouched. A gap latches until a fresh
// snapshot is installed. Its owner serializes calls in the receive loop.
type Sequence struct {
	cursor      Cursor
	epochFloor  uint64
	initialized bool
	waiting     bool
}

func (s *Sequence) Cursor() (Cursor, bool) { return s.cursor, s.initialized }

func (s *Sequence) Snapshot(c Cursor) error {
	if err := c.Validate(); err != nil {
		return err
	}
	epoch := epochNumber(c.Epoch)
	if epoch < s.epochFloor || (s.initialized && c.Epoch == s.cursor.Epoch && c.Seq < s.cursor.Seq) {
		return ErrStaleCursor
	}
	s.cursor, s.epochFloor, s.initialized, s.waiting = c, epoch, true, false
	return nil
}

func (s *Sequence) Accept(c Cursor) (bool, error) {
	if err := c.Validate(); err != nil {
		return false, err
	}
	epoch := epochNumber(c.Epoch)
	if epoch < s.epochFloor {
		return false, nil
	}
	if epoch > s.epochFloor {
		s.epochFloor, s.waiting = epoch, true
	}
	if !s.initialized || s.waiting || c.Epoch != s.cursor.Epoch {
		s.waiting = true
		return false, ErrResyncRequired
	}
	if c.Seq <= s.cursor.Seq {
		return false, nil
	}
	if c.Seq-s.cursor.Seq != 1 {
		s.waiting = true
		return false, ErrResyncRequired
	}
	s.cursor = c
	return true, nil
}
