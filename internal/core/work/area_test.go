// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}
func areaGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append([]string{"PATH=" + os.Getenv("PATH")}, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}
func areaFixture(t *testing.T) (*Area, WorkspaceSpec, string) {
	t.Helper()
	source := t.TempDir()
	areaGit(t, source, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	areaGit(t, source, "add", ".")
	areaGit(t, source, "commit", "-m", "base")
	base := areaGit(t, source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "later.txt"), []byte("later\n"), 0600); err != nil {
		t.Fatal(err)
	}
	areaGit(t, source, "add", ".")
	areaGit(t, source, "commit", "-m", "later")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	a, err := OpenArea(t.TempDir(), git)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(context.Background()) })
	return a, WorkspaceSpec{RepositoryID: "repo", Source: source, Base: base, Branch: "task/task-1"}, source
}
func TestAreaPinnedWorktreeAndProtectedRefs(t *testing.T) {
	a, s, source := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	w, err := a.Prepare(t.Context(), l, "task-1", s)
	if err != nil {
		t.Fatal(err)
	}
	if w.Path != filepath.Join(a.path, "tasks", "task-1") || areaGit(t, w.Path, "rev-parse", "HEAD") != s.Base {
		t.Fatalf("wrong allocation: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(w.Path, "later.txt")); !os.IsNotExist(err) {
		t.Fatal("used moving branch tip")
	}
	areaGit(t, w.Path, "switch", "-c", s.Branch)
	if err := os.WriteFile(filepath.Join(w.Path, "result.txt"), []byte("result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	areaGit(t, w.Path, "add", ".")
	areaGit(t, w.Path, "commit", "-m", "result")
	head := areaGit(t, w.Path, "rev-parse", "HEAD")
	for _, ref := range []string{"refs/heads/main", "refs/heads/dev", "refs/remotes/origin/main", "refs/lina/base/task-1"} {
		c := exec.Command("git", "update-ref", ref, head)
		c.Dir = w.Path
		if out, err := c.CombinedOutput(); err == nil {
			t.Fatalf("protected ref %s changed: %s", ref, out)
		}
	}
	if areaGit(t, source, "rev-parse", "HEAD") == head {
		t.Fatal("source repository changed")
	}
	again, err := a.Prepare(t.Context(), l, "task-1", s)
	if err != nil || again != w {
		t.Fatalf("retry: %+v %v", again, err)
	}
	if areaGit(t, w.Path, "rev-parse", "HEAD") != head {
		t.Fatal("retry reset worker work")
	}
	bundle, err := a.Bundle(t.Context(), l, w, head)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(bundle)
	restored := t.TempDir()
	areaGit(t, restored, "clone", "-b", s.Branch, bundle, "copy")
	if got := areaGit(t, filepath.Join(restored, "copy"), "rev-parse", s.Branch); got != head {
		t.Fatal("bundle cannot recover branch")
	}
}
func TestAreaRefusesEscapesAndConflictingAllocation(t *testing.T) {
	a, s, _ := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	for _, id := range []string{"../outside", "/outside", ".", "a/b", "--flag"} {
		if _, err := a.Prepare(t.Context(), l, id, s); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	for _, base := range []string{"main", "HEAD", "--all"} {
		bad := s
		bad.Base = base
		if _, err := a.Prepare(t.Context(), l, "task-1", bad); err == nil {
			t.Fatal("accepted unpinned base")
		}
	}
	bad := s
	bad.Branch = "main"
	if _, err := a.Prepare(t.Context(), l, "task-1", bad); err == nil {
		t.Fatal("protected task branch")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(a.path, "tasks", "task-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prepare(t.Context(), l, "task-1", s); err == nil {
		t.Fatal("followed task symlink")
	}
}

func TestAreaNewBaseDoesNotResetOtherTask(t *testing.T) {
	a, s, source := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	first, err := a.Prepare(t.Context(), l, "task-1", s)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(first.Path, "draft.txt")
	if err = os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	areaGit(t, source, "add", ".")
	areaGit(t, source, "commit", "-m", "new base")
	s.Base = areaGit(t, source, "rev-parse", "HEAD")
	s.Branch = "task/task-2"
	second, err := a.Prepare(t.Context(), l, "task-2", s)
	if err != nil {
		t.Fatal(err)
	}
	if areaGit(t, second.Path, "rev-parse", "HEAD") != s.Base {
		t.Fatal("new source base not fetched")
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != "keep" {
		t.Fatal("other task was reset")
	}
}

func TestAreaRefusesReplacedHookWithoutOverwritingTarget(t *testing.T) {
	a, s, _ := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	if _, err := a.Prepare(t.Context(), l, "task-1", s); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(a.path, "clones", s.RepositoryID, "hooks/reference-transaction")
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, hook); err != nil {
		t.Fatal(err)
	}
	s.Branch = "task/task-2"
	if _, err := a.Prepare(t.Context(), l, "task-2", s); err == nil {
		t.Error("accepted substituted hook")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep" {
		t.Fatal("overwrote linked file")
	}
}
