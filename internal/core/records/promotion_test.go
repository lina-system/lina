// SPDX-License-Identifier: Apache-2.0
package records

import (
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"strings"
	"testing"
)

func TestMemoPromotionUndoAndStaleProtection(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	c := Change{ID: "memo", Path: "inbox/notes.md", Data: []byte("---\nid: \"memo\"\nkind: memo\nstate: active\nperson_id: \"person\"\n---\n\n# Plan the visit\n\nKeep this note.\n")}
	rev := commit(t, s, c)
	got, err := s.PromoteMemo(t.Context(), actor, c.ID, rev)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.Undo == "" || got.Notice == "" || !strings.Contains(got.Markdown, "Keep this note.") {
		t.Fatal(got)
	}
	rec, _, _ := s.Read(t.Context(), c.ID)
	if rec.Path != c.Path {
		t.Fatal("promotion moved memo", rec.Path)
	}
	if _, err = s.PromoteMemo(t.Context(), actor, c.ID, rev); !errors.Is(err, ErrStaleRevision) {
		t.Fatal(err)
	}
	restored, err := s.UndoRecord(t.Context(), actor, got.Undo)
	if err != nil || restored.Revision == rev || restored.Markdown != string(c.Data) {
		t.Fatal(restored, err)
	}
	again, err := s.UndoRecord(t.Context(), actor, got.Undo)
	if err != nil || again.Revision != restored.Revision {
		t.Fatal("undo replay", again, err)
	}
	got, err = s.PromoteMemo(t.Context(), actor, c.ID, restored.Revision)
	if err != nil {
		t.Fatal(err)
	}
	rec, rev, _ = s.Read(t.Context(), c.ID)
	commit(t, s, Change{ID: c.ID, Base: rev, Path: rec.Path, Data: []byte(strings.Replace(string(rec.Data), "Keep this note.", "New note.", 1))})
	if _, err = s.UndoRecord(t.Context(), actor, got.Undo); !errors.Is(err, ErrStaleRevision) {
		t.Fatal("undo overwrote later edit", err)
	}
	if _, err = s.UndoRecord(t.Context(), Actor{Person: "other", Agent: "agent"}, got.Undo); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign undo", err)
	}
}

func TestMemoPromotionCrashRecoveryKeepsUndo(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"intent", "file", "ref", "checkpoint"} {
		t.Run(stage, func(t *testing.T) {
			s, root, db := setup(t)
			c := Change{ID: "memo", Path: "inbox/memo.md", Data: []byte("---\nid: \"memo\"\nkind: memo\nstate: active\nperson_id: \"person\"\n---\n\n# Note\n")}
			rev := commit(t, s, c)
			s.PublicationHook = func(at string) error {
				if at == stage {
					return errors.New("synthetic interruption")
				}
				return nil
			}
			if _, err := s.PromoteMemo(t.Context(), actor, c.ID, rev); !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
			recovered, err := Open(root, db)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			var handle string
			if err = db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT handle FROM record_operations WHERE record_id=?", c.ID).Scan(&handle)
			}); err != nil {
				t.Fatal(err)
			}
			restored, err := recovered.UndoRecord(t.Context(), actor, handle)
			if err != nil || restored.Markdown != string(c.Data) {
				t.Fatal(restored, err)
			}
		})
	}
}
