// SPDX-License-Identifier: Apache-2.0

package rpc

import (
	"errors"
	"testing"
)

func TestCursorValidation(t *testing.T) {
	for _, epoch := range []string{"", "01", "+1", "-1", " 1", "18446744073709551616"} {
		if (Cursor{Epoch: epoch}).Validate() == nil {
			t.Errorf("accepted epoch %q", epoch)
		}
	}
	for _, epoch := range []string{"0", "9", "10", "18446744073709551615"} {
		if err := (Cursor{Epoch: epoch}).Validate(); err != nil {
			t.Errorf("epoch %q: %v", epoch, err)
		}
	}
}

func TestSequenceGapSnapshotAndDoubleReplay(t *testing.T) {
	var s Sequence
	if err := s.Snapshot(Cursor{"9", 3}); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		for seq := uint64(4); seq <= 5; seq++ {
			applied, err := s.Accept(Cursor{"9", seq})
			if err != nil || applied != (pass == 0) {
				t.Fatalf("pass %d seq %d: %v %v", pass, seq, applied, err)
			}
		}
	}
	if applied, err := s.Accept(Cursor{"9", 7}); applied || !errors.Is(err, ErrResyncRequired) {
		t.Fatalf("gap: %v %v", applied, err)
	}
	if applied, err := s.Accept(Cursor{"9", 6}); applied || !errors.Is(err, ErrResyncRequired) {
		t.Fatalf("gap must latch: %v %v", applied, err)
	}
	if err := s.Snapshot(Cursor{"9", 7}); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.Accept(Cursor{"9", 8}); !applied || err != nil {
		t.Fatalf("recovered: %v %v", applied, err)
	}
	if err := s.Snapshot(Cursor{"9", 7}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("stale snapshot: %v", err)
	}
	if applied, err := s.Accept(Cursor{"10", 1}); applied || !errors.Is(err, ErrResyncRequired) {
		t.Fatalf("epoch change: %v %v", applied, err)
	}
	if err := s.Snapshot(Cursor{"10", 0}); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.Accept(Cursor{"9", 100}); applied || err != nil {
		t.Fatalf("old epoch must be ignored: %v %v", applied, err)
	}
	if err := s.Snapshot(Cursor{"9", 100}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("numeric epoch comparison: %v", err)
	}
}

func TestSequenceRequiresSnapshotAndDoesNotWrap(t *testing.T) {
	var s Sequence
	if _, err := s.Accept(Cursor{"1", 1}); !errors.Is(err, ErrResyncRequired) {
		t.Fatal(err)
	}
	if err := s.Snapshot(Cursor{"1", ^uint64(0)}); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.Accept(Cursor{"1", 0}); applied || err != nil {
		t.Fatalf("wrapped duplicate: %v %v", applied, err)
	}
}
