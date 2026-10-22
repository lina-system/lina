// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func setup(t *testing.T) (*Store, string, Canon) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root, db
}

var actor = Actor{Person: "person", Agent: "agent"}

func doc(id, title string) []byte {
	return []byte("---\nid: \"" + id + "\"\nkind: memo\nstate: active\n---\n\n# " + title + "\n")
}
func commit(t *testing.T, s Repo, c Change) Rev {
	t.Helper()
	rev, err := s.Commit(t.Context(), c, actor)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}
func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", filepath.Join(root, "versions/records")}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, raw, err)
	}
	return strings.TrimSpace(string(raw))
}
func TestRevisionMoveAndOneCommitPerWrite(t *testing.T) {
	s, root, _ := setup(t)
	c := Change{ID: "stable", Path: "inbox/notes.md", Data: doc("stable", "Notes")}
	first := commit(t, s, c)
	commit(t, s, Change{ID: "other", Path: "inbox/other.md", Data: doc("other", "Other")})
	record, rev, err := s.Read(t.Context(), "stable")
	if err != nil || rev != first || record.Path != c.Path {
		t.Fatal(record, rev, err)
	}
	c.Base = first
	c.Data = doc("stable", "Revised")
	second := commit(t, s, c)
	if _, err := s.Commit(t.Context(), c, actor); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale write: %v", err)
	}
	c.Base = second
	c.Path = "resources/moved.md"
	third := commit(t, s, c)
	got, rev, err := s.Read(t.Context(), "stable")
	if err != nil || got.ID != "stable" || got.Path != c.Path || rev != third {
		t.Fatal(got, rev, err)
	}
	if _, err := os.Stat(filepath.Join(root, "versions/records/inbox/notes.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old file survives", err)
	}
	if got := git(t, root, "rev-list", "--count", "HEAD"); got != "4" {
		t.Fatal("commit count", got)
	}
	if got := git(t, root, "status", "--porcelain"); got != "" {
		t.Fatal("dirty repository", got)
	}
	git(t, root, "fsck", "--strict")
	if msg := git(t, root, "log", "-1", "--format=%B"); !strings.Contains(msg, `Person: "person"`) || !strings.Contains(msg, `Agent: "agent"`) {
		t.Fatal(msg)
	}
}
func TestConcurrentRevisionAndOutsideEdit(t *testing.T) {
	s, root, _ := setup(t)
	c := Change{ID: "stable", Path: "inbox/notes.md", Data: doc("stable", "Notes")}
	c.Base = commit(t, s, c)
	c.Data = doc("stable", "Changed")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.Commit(t.Context(), c, actor); errs <- err })
	}
	wg.Wait()
	close(errs)
	success, stale := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrStaleRevision) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatal(success, stale)
	}
	name := filepath.Join(root, "versions/records", c.Path)
	if err := os.WriteFile(name, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Read(t.Context(), c.ID); !errors.Is(err, ErrRecordsChanged) {
		t.Fatal(err)
	}
	if _, err := s.Commit(t.Context(), c, actor); !errors.Is(err, ErrRecordsChanged) {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(name)
	if string(raw) != "outside" {
		t.Fatal("overwritten")
	}
}
func TestRejectInvalidIdentityAndPaths(t *testing.T) {
	s, _, _ := setup(t)
	for _, name := range []string{"../escape.md", ".git/config", "inbox/../notes.md", "inbox/link/file.md"} {
		if name == "inbox/link/file.md" {
			if err := s.root.Mkdir("inbox", 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(s.root.Name(), "inbox/link")); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Commit(t.Context(), Change{ID: "stable", Path: name, Data: doc("stable", "Notes")}, actor); err == nil {
			t.Fatal("accepted", name)
		}
	}
	if _, err := s.Commit(t.Context(), Change{ID: "stable", Path: "inbox/notes.md", Data: doc("different", "Notes")}, actor); err == nil {
		t.Fatal("changed id accepted")
	}
}

func TestRecoveryEditsAndMovesAtEveryBoundary(t *testing.T) {
	for _, move := range []bool{false, true} {
		for _, stage := range []string{"lock", "intent", "file", "remove", "index", "ref", "checkpoint"} {
			t.Run(fmt.Sprintf("move=%v/%s", move, stage), func(t *testing.T) {
				s, root, db := setup(t)
				c := Change{ID: "stable", Path: "inbox/notes.md", Data: doc("stable", "Original")}
				first := commit(t, s, c)
				c.Base = first
				c.Data = doc("stable", "Updated")
				if move {
					c.Path = "resources/notes.md"
				}
				injected := errors.New("interrupted")
				s.PublicationHook = func(point string) error {
					if point == stage {
						return injected
					}
					return nil
				}
				if _, err := s.Commit(t.Context(), c, actor); !errors.Is(err, injected) {
					t.Fatal("missing fault", err)
				}
				s.Close()
				reopened, err := Open(root, db)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				got, rev, err := reopened.Read(t.Context(), c.ID)
				if err != nil {
					t.Fatal(err)
				}
				count := "2"
				if stage == "lock" {
					count = "1"
					if rev != first || string(got.Data) != string(doc("stable", "Original")) {
						t.Fatal("lock-only failure wrote content")
					}
				} else if rev == first || got.Path != c.Path || string(got.Data) != string(c.Data) {
					t.Fatal(got, rev)
				}
				if got := git(t, root, "rev-list", "--count", "HEAD"); got != count {
					t.Fatal("recovery duplicated a commit", got, count)
				}
				if got := git(t, root, "status", "--porcelain"); got != "" {
					t.Fatal("dirty recovery", got)
				}
				git(t, root, "fsck", "--strict")
				// Rebuilding the disposable index yields exactly the same live locators.
				snapshot := func() string {
					var rows []indexRow
					err := db.Read(t.Context(), func(tx *canon.Tx) error {
						rs, err := tx.QueryContext(t.Context(), "SELECT id,path,blob,revision FROM records_index ORDER BY id")
						if err != nil {
							return err
						}
						defer rs.Close()
						for rs.Next() {
							var r indexRow
							if err = rs.Scan(&r.id, &r.path, &r.blob, &r.revision); err != nil {
								return err
							}
							rows = append(rows, r)
						}
						return rs.Err()
					})
					if err != nil {
						t.Fatal(err)
					}
					return fmt.Sprint(rows)
				}
				before := snapshot()
				if err = reopened.Rebuild(t.Context()); err != nil {
					t.Fatal(err)
				}
				if after := snapshot(); before != after {
					t.Fatal("rebuild differs", before, after)
				}
			})
		}
	}
}
func TestRecoveryDoesNotOverwriteOutsideDeletion(t *testing.T) {
	s, root, db := setup(t)
	c := Change{ID: "stable", Path: "inbox/notes.md", Data: doc("stable", "Original")}
	c.Base = commit(t, s, c)
	c.Data = doc("stable", "Updated")
	s.PublicationHook = func(stage string) error {
		if stage == "file" {
			return errors.New("interrupted")
		}
		return nil
	}
	if _, err := s.Commit(t.Context(), c, actor); err == nil {
		t.Fatal("missing fault")
	}
	s.Close()
	target := filepath.Join(root, "versions/records", c.Path)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, db)
	if reopened != nil {
		reopened.Close()
	}
	if !errors.Is(err, ErrRecordsChanged) {
		t.Fatalf("outside deletion silently restored: %v", err)
	}
	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("outside deletion overwritten", err)
	}
}
