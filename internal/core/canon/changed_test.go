// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"errors"
	"testing"
)

func TestChangedOnlyAfterCommittedMutation(t *testing.T) {
	t.Parallel()
	s := openStore(t, storeConfig(t))
	changed := s.Changed()
	aborted := errors.New("abort")
	if err := s.Write(t.Context(), func(tx *Tx) error {
		if err := insertItem(tx); err != nil {
			return err
		}
		return aborted
	}); !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Fatal("rollback notified")
	default:
	}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		var n int
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM items").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Fatal("read notified")
	default:
	}
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("commit not notified")
	}
	select {
	case <-s.Changed():
		t.Fatal("new subscriber got stale event")
	default:
	}
}
