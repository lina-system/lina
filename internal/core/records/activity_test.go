// SPDX-License-Identifier: Apache-2.0
package records

import (
	"github.com/thisisjun786/lina/internal/core/canon"
	"testing"
	"time"
)

func TestActivityRebuiltFromRecordRevision(t *testing.T) {
	t.Parallel()
	s, root, db := setup(t)
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return at }
	commit(t, s, Change{ID: "first", Path: "inbox/first.md", Data: doc("first", "First")})
	at = at.Add(time.Hour)
	commit(t, s, Change{ID: "second", Path: "inbox/second.md", Data: doc("second", "Second")})
	check := func() {
		t.Helper()
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			for n, id := range []string{"first", "second"} {
				var value string
				if err := tx.QueryRowContext(t.Context(), "SELECT last_activity FROM records_activity WHERE id=?", id).Scan(&value); err != nil {
					return err
				}
				want := time.Date(2026, 1, 1, 10+n, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
				if value != want {
					t.Errorf("%s: %s, want %s", id, value, want)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM records_index")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check()
}
