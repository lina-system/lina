// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestGitRevisionIsReadableAndCorruptionBlocksPreparation(t *testing.T) {
	s, db, root := setup(t)
	a := create(t, s, "")
	first, err := s.Prepare(t.Context(), "first", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	layers := first.Layers
	layers.GrowthSnapshot = "experience"
	a, err = s.Revise(t.Context(), a.ID, a.Revision, layers, "person")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Prepare(t.Context(), "next", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "versions", "agents", a.ID)
	cmd := exec.CommandContext(t.Context(), "git", "--git-dir="+dir, "fsck", "--strict", "--no-reflogs")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fsck: %s %v", out, err)
	}
	cmd = exec.CommandContext(t.Context(), "git", "--git-dir="+dir, "rev-parse", next.Commit+"^")
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != first.Commit {
		t.Fatalf("parent: %s %v", out, err)
	}
	cmd = exec.CommandContext(t.Context(), "git", "--git-dir="+dir, "show", next.Commit+":persona.json")
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "experience") {
		t.Fatalf("persona: %s %v", out, err)
	}
	path := filepath.Join(dir, "objects", next.Commit[:2], next.Commit[2:])
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(t.Context(), "broken", a.ID); !errors.Is(err, agents.ErrCorrupt) {
		t.Fatalf("corruption: %v", err)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM agent_preparations WHERE turn_id='broken'").Scan(&n)
		if n != 0 {
			t.Fatal("acknowledged corrupt bytes")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCompetingRevisionsCannotReplaceEachOther(t *testing.T) {
	s, db, root := setup(t)
	a := create(t, s, "")
	second, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*agents.Store{s, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.Revise(t.Context(), a.ID, a.Revision, agents.Layers{Core: []string{"one", "two"}[i]}, "person")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, agents.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	if got, err := s.Get(t.Context(), a.ID); err != nil || got.Revision != 2 {
		t.Fatalf("revision: %+v %v", got, err)
	}
}
func TestDeletionAndPresetFanoutRollBackAsOneWrite(t *testing.T) {
	s, db, _ := setup(t)
	p := agents.Preset{ID: "shared", Core: "old"}
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	a, b := create(t, s, p.ID), create(t, s, p.ID)
	if err := s.Bind(t.Context(), "channel", a.ID, "person"); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER refuse_binding_delete BEFORE DELETE ON agent_bindings BEGIN SELECT RAISE(ABORT,'injected'); END; CREATE TRIGGER refuse_revision BEFORE INSERT ON agent_revisions WHEN NEW.revision=2 BEGIN SELECT RAISE(ABORT,'injected'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), a.ID, "person", time.Now()); err == nil {
		t.Fatal("deletion succeeded")
	}
	if _, err := s.Get(t.Context(), a.ID); err != nil {
		t.Fatal("partial deletion", err)
	}
	if got, err := s.ResolveBinding(t.Context(), "channel"); err != nil || got != a.ID {
		t.Fatal("lost binding", err)
	}
	p.Core = "new"
	if err := s.UpdatePreset(t.Context(), p, "person"); err == nil {
		t.Fatal("preset succeeded")
	}
	for _, a := range []agents.Agent{a, b} {
		got, err := s.Prepare(t.Context(), a.ID, a.ID)
		if err != nil || got.Revision != 1 || got.Layers.Core != "old" {
			t.Fatalf("partial preset: %+v %v", got, err)
		}
	}
	c := create(t, s, p.ID)
	got, err := s.Prepare(t.Context(), c.ID, c.ID)
	if err != nil || got.Layers.Core != "old" {
		t.Fatalf("preset catalog changed: %+v %v", got, err)
	}
}
func TestReadOnlyWriterCannotPublishRevision(t *testing.T) {
	s, _, root := setup(t)
	a := create(t, s, "")
	db, err := canon.OpenReader(t.Context(), filepath.Join(root, "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	reader, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if got, err := reader.Revise(t.Context(), a.ID, a.Revision, agents.Layers{Core: "unpublished"}, "person"); !errors.Is(err, canon.ErrReadOnly) || got.ID != "" {
		t.Fatalf("write: %+v %v", got, err)
	}
	got, err := s.Prepare(t.Context(), "live", a.ID)
	if err != nil || got.Revision != 1 || got.Layers.Core != "core" {
		t.Fatalf("live: %+v %v", got, err)
	}
}
func TestConcurrentFirstRunCreatesOneAgent(t *testing.T) {
	s, _, _ := setup(t)
	start := make(chan struct{})
	results := make(chan agents.FirstRun, 4)
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			<-start
			result, err := s.FirstRun(t.Context(), "person", false)
			results <- result
			errs <- err
		}()
	}
	close(start)
	var id string
	for range 4 {
		result := <-results
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if id != "" && id != result.Agent.ID {
			t.Fatal("duplicate default")
		}
		id = result.Agent.ID
	}
}
func TestVersionPathCannotEscapeStateRoot(t *testing.T) {
	s, db, root := setup(t)
	a := create(t, s, "")
	outside := t.TempDir()
	path := filepath.Join(root, "versions", "agents", a.ID)
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revise(t.Context(), a.ID, a.Revision, agents.Layers{Core: "escape"}, "person"); err == nil {
		t.Fatal("followed escaping symlink")
	}
	if files, err := os.ReadDir(outside); err != nil || len(files) != 0 {
		t.Fatal("wrote outside root", err)
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
}
