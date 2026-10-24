// SPDX-License-Identifier: Apache-2.0
package skills

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func maintenanceSkill(t *testing.T, s *Store, id, body string) Applied {
	t.Helper()
	a, err := s.Patch(t.Context(), Patch{ID: id, Files: Bundle{"SKILL.md": {Text: strings.Replace(document(body), "name: example", "name: "+id, 1)}}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func maintain(t *testing.T, s *Store, c Maintenance) (Applied, error) {
	t.Helper()
	var a Applied
	err := s.db.Write(t.Context(), func(tx *canon.Tx) error { var err error; a, err = s.MaintainTx(t.Context(), tx, c); return err })
	return a, err
}
func TestMaintenanceMergeUndoAndSuppression(t *testing.T) {
	t.Parallel()
	s, db, root := fixture(t, nil)
	a := maintenanceSkill(t, s, "alpha", "Check input.")
	b := maintenanceSkill(t, s, "beta", "Check output.")
	c := Maintenance{Kind: "merge", Target: a.Undo, Sources: []Undo{b.Undo}, Files: Bundle{"SKILL.md": {Text: strings.Replace(document("Check input and output."), "name: example", "name: alpha", 1)}}, Reason: "Combined matching procedures."}
	merged, err := maintain(t, s, c)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.Catalog(t.Context())
	if err != nil || len(catalog) != 1 || catalog[0] != merged.Version || merged.Notice == "" || strings.ContainsAny(merged.Notice, "\r\n") {
		t.Fatalf("merge=%+v catalog=%+v err=%v", merged, catalog, err)
	}
	if old, err := s.Read(t.Context(), b.Version); err != nil || !strings.Contains(old.Files["SKILL.md"].Text, "Check output.") {
		t.Fatalf("lost source: %+v %v", old, err)
	}
	restored, err := s.Undo(t.Context(), merged.Undo)
	if err != nil || restored.Version.Revision != 3 {
		t.Fatalf("undo=%+v %v", restored, err)
	}
	catalog, err = s.Catalog(t.Context())
	if err != nil || len(catalog) != 2 {
		t.Fatalf("undo catalog=%+v %v", catalog, err)
	}
	for _, v := range catalog {
		original := a.Version
		if v.ID == "beta" {
			original = b.Version
		}
		old, _ := s.Read(t.Context(), original)
		now, e := s.Read(t.Context(), v)
		if e != nil || !reflect.DeepEqual(old.Files, now.Files) {
			t.Fatalf("restore %s: %v", v.ID, e)
		}
	}
	reopened, err := Open(root, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c.Target.Revision = 3
	c.Sources[0].Revision = 3
	c.Reason = "Reworded reason."
	if _, err := maintain(t, reopened, c); !errors.Is(err, ErrUndone) {
		t.Fatalf("repeated merge: %v", err)
	}
	// Reversing the destination and changing the generated wording must not evade undo.
	c.Target, c.Sources[0] = c.Sources[0], c.Target
	c.Files = Bundle{"SKILL.md": {Text: strings.Replace(document("Check both ends."), "name: example", "name: beta", 1)}}
	if _, err := maintain(t, reopened, c); !errors.Is(err, ErrUndone) {
		t.Fatalf("reversed merge: %v", err)
	}
}
func TestMaintenancePruneBuiltinAndAtomicity(t *testing.T) {
	t.Parallel()
	s, _, _ := fixture(t, fstest.MapFS{"example/SKILL.md": {Data: []byte(document("Builtin procedure."))}})
	a := maintenanceSkill(t, s, "alpha", "Obsolete procedure.")
	b := maintenanceSkill(t, s, "beta", "Another procedure.")
	before, _ := s.Catalog(t.Context())
	for _, c := range []Maintenance{
		{Kind: "prune", Target: Undo{ID: "example"}, Reason: "Obsolete."},
		{Kind: "merge", Target: a.Undo, Sources: []Undo{{ID: "example"}}, Files: Bundle{"SKILL.md": {Text: strings.Replace(document("Merged."), "name: example", "name: alpha", 1)}}, Reason: "Overlap."},
		{Kind: "merge", Target: a.Undo, Sources: []Undo{{ID: "beta", Revision: 99}}, Files: Bundle{"SKILL.md": {Text: strings.Replace(document("Merged."), "name: example", "name: alpha", 1)}}, Reason: "Overlap."},
		{Kind: "merge", Target: a.Undo, Sources: []Undo{b.Undo}, Files: Bundle{"run.sh": {Text: "echo no"}}, Reason: "Overlap."},
	} {
		if _, err := maintain(t, s, c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
		after, _ := s.Catalog(t.Context())
		if !reflect.DeepEqual(before, after) {
			t.Fatal("partial change")
		}
	}
	p, err := maintain(t, s, Maintenance{Kind: "prune", Target: a.Undo, Reason: "Replaced procedure."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(t.Context(), p.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("prune remains readable: %v", err)
	}
	if _, err := s.Undo(t.Context(), p.Undo); err != nil {
		t.Fatal(err)
	}
	if _, err := maintain(t, s, Maintenance{Kind: "prune", Target: Undo{ID: "alpha", Revision: 3}, Reason: "Again."}); !errors.Is(err, ErrUndone) {
		t.Fatalf("reprune: %v", err)
	}
}

func TestMaintenanceRollbackAndUndoConflict(t *testing.T) {
	t.Parallel()
	s, db, _ := fixture(t, nil)
	a := maintenanceSkill(t, s, "alpha", "Original A.")
	b := maintenanceSkill(t, s, "beta", "Original B.")
	c := Maintenance{Kind: "merge", Target: a.Undo, Sources: []Undo{b.Undo}, Files: Bundle{"SKILL.md": {Text: strings.Replace(document("Combined."), "name: example", "name: alpha", 1)}}, Reason: "Same purpose."}
	before, _ := s.Catalog(t.Context())
	injected := errors.New("injected commit failure")
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := s.MaintainTx(t.Context(), tx, c); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	after, _ := s.Catalog(t.Context())
	notices, _ := s.MaintenanceNotices(t.Context())
	if !reflect.DeepEqual(before, after) || len(notices) != 0 {
		t.Fatal("rolled-back merge was published")
	}
	merged, err := maintain(t, s, c)
	if err != nil {
		t.Fatal(err)
	}
	// Restore a source through an unrelated edit. A group undo must not overwrite it.
	_, err = s.Patch(t.Context(), Patch{ID: "beta", Expected: 2, Files: Bundle{"SKILL.md": {Text: strings.Replace(document("New user work."), "name: example", "name: beta", 1)}}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ = s.Catalog(t.Context())
	if _, err = s.Undo(t.Context(), merged.Undo); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale group undo=%v", err)
	}
	after, _ = s.Catalog(t.Context())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stale undo partially restored members")
	}
}

func TestMaintenanceProtectsPatchedBuiltinsAndUnchangedMergeTarget(t *testing.T) {
	t.Parallel()
	s, _, _ := fixture(t, fstest.MapFS{"example/SKILL.md": {Data: []byte(document("Builtin."))}})
	overlay := apply(t, s, 0, "Changed builtin.")
	if _, err := maintain(t, s, Maintenance{Kind: "prune", Target: overlay.Undo, Reason: "No."}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("patched builtin pruned: %v", err)
	}
	a := maintenanceSkill(t, s, "alpha", "Identical instructions.")
	b := maintenanceSkill(t, s, "beta", "Identical instructions.")
	body, _ := s.Read(t.Context(), a.Version)
	merged, err := maintain(t, s, Maintenance{Kind: "merge", Target: a.Undo, Sources: []Undo{b.Undo}, Files: body.Files, Reason: "Duplicates."})
	if err != nil {
		t.Fatal(err)
	}
	// Either member's handle restores the whole merge, including when its target
	// already contained the combined instructions and did not change content.
	if _, err = s.Undo(t.Context(), Undo{ID: "beta", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	catalog, _ := s.Catalog(t.Context())
	if len(catalog) != 3 {
		t.Fatalf("catalog=%+v merge=%+v", catalog, merged)
	}
}
