// SPDX-License-Identifier: Apache-2.0
package records

import (
	"bytes"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"testing"
)

func TestCitationProjectionRebuildAndSourceRemoval(t *testing.T) {
	s, _, db := setup(t)
	document := func(source string) []byte {
		return bytes.Replace(doc("note", "Notes"), []byte("kind: memo"), []byte("kind: memo\nsource: "+source), 1)
	}
	rev := commit(t, s, Change{ID: "note", Path: "inbox/note.md", Data: document("\"https://example.com/source\"")})
	check := func(want int) {
		t.Helper()
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM cite WHERE view_kind='record'").Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Fatalf("record citations=%d, want %d", n, want)
			}
			if want == 1 {
				var id, revision, kind string
				if err := tx.QueryRowContext(t.Context(), "SELECT target_id,added_revision,target_kind FROM cite WHERE view_kind='record'").Scan(&id, &revision, &kind); err != nil {
					return err
				}
				if id != "https://example.com/source" || revision != string(rev) || kind != "external" {
					t.Fatalf("citation=%s/%s/%s", id, revision, kind)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check(1)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM records_index")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(0)
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	check(1)
	rev = commit(t, s, Change{ID: "note", Path: "inbox/note.md", Base: rev, Data: doc("note", "No source")})
	check(0)
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	check(0)
}

func TestCitationProjectionUsesRetainedEventIdentity(t *testing.T) {
	s, _, db := setup(t)
	seedCandidateSource(t, db, "source-event", "Retained source", "person", guard.EntryOwner)
	data := bytes.Replace(doc("event-note", "Notes"), []byte("kind: memo"), []byte("kind: memo\nsource: \"source-event\""), 1)
	commit(t, s, Change{ID: "event-note", Path: "inbox/event.md", Data: data})
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM cite WHERE view_kind='record' AND view_id='event-note' AND target_kind='event' AND target_id='source-event' AND precision='event' AND role='cites'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("retained event citations=%d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
