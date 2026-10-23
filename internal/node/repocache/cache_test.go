// SPDX-License-Identifier: Apache-2.0

package repocache

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

func gitPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func testLease(t *testing.T) lease.Lease {
	t.Helper()
	l := lease.New(func() {})
	t.Cleanup(l.Release)
	return l
}

func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixture(t *testing.T) (*Cache, Request, string) {
	t.Helper()
	upstream := t.TempDir()
	gitFixture(t, upstream, "init", "--initial-branch=dev")
	if err := os.WriteFile(filepath.Join(upstream, "result.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, upstream, "add", "result.txt")
	gitFixture(t, upstream, "commit", "-m", "fixture")
	cache, err := Open(filepath.Join(t.TempDir(), "cache"), gitPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// Only tests substitute the GitHub remote, using real Git and synthetic data.
	cache.remote = func(Repository) string { return "file://" + upstream }
	cache.protocols = "https:file"
	return cache, Request{Repository: Repository{Owner: "fixture", Name: "repo"}, Task: "task-one", Attempt: "attempt-one", Base: "dev", Branch: "task/one"}, upstream
}

func TestCloneOnDemandAndIsolateTasks(t *testing.T) {
	c, req, _ := fixture(t)
	entries, err := os.ReadDir(c.root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("opening a cache cloned a repository: %v %v", entries, err)
	}
	first, err := c.Prepare(t.Context(), testLease(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if text, err := os.ReadFile(filepath.Join(first.Path, "result.txt")); err != nil || string(text) != "base\n" {
		t.Fatalf("checkout: %q %v", text, err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "result.txt"), []byte("first task\n"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Task, req.Attempt, req.Branch = "task-two", "attempt-two", "task/two"
	second, err := c.Prepare(t.Context(), testLease(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.RepositoryPath != second.RepositoryPath || first.Path == second.Path {
		t.Fatal("tasks did not share the cache and isolate their worktrees")
	}
	if text, _ := os.ReadFile(filepath.Join(second.Path, "result.txt")); string(text) != "base\n" {
		t.Fatalf("task crossed worktree: %q", text)
	}
}

func TestInterruptedPreparationLeavesCacheUsable(t *testing.T) {
	c, req, _ := fixture(t)
	first, err := c.Prepare(t.Context(), testLease(t), req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req.Task, req.Branch = "interrupted", "task/interrupted"
	if _, err := c.Prepare(ctx, testLease(t), req); err == nil {
		t.Fatal("cancelled preparation succeeded")
	}
	req.Task, req.Branch = "next", "task/next"
	next, err := c.Prepare(t.Context(), testLease(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if next.RepositoryPath != first.RepositoryPath {
		t.Fatal("usable cache was replaced")
	}
	gitFixture(t, first.RepositoryPath, "fsck", "--full")
}

func TestExistingTaskIsPreserved(t *testing.T) {
	c, req, _ := fixture(t)
	first, err := c.Prepare(t.Context(), testLease(t), req)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(first.Path, "uncommitted.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prepare(t.Context(), testLease(t), req); err == nil {
		t.Fatal("existing attempt was silently reused")
	}
	if text, _ := os.ReadFile(path); string(text) != "keep me" {
		t.Fatal("existing task work was lost")
	}
}

func TestRejectNonGitHubRepository(t *testing.T) {
	for _, repo := range []Repository{{Owner: "../core", Name: "repo"}, {Owner: "fixture", Name: "../repo"}, {Owner: "https://other.invalid", Name: "repo"}, {Owner: "fixture", Name: "repo.git?token=secret"}} {
		if _, err := repo.URL(); err == nil {
			t.Errorf("accepted non-GitHub repository: %#v", repo)
		}
	}
}

func TestRemoveOnlyConfirmedMergedCheckout(t *testing.T) {
	c, req, _ := fixture(t)
	first, err := c.Prepare(t.Context(), testLease(t), req)
	if err != nil {
		t.Fatal(err)
	}
	head := gitFixture(t, first.Path, "rev-parse", "HEAD")
	merged := MergedPullRequest{Repository: req.Repository, Branch: req.Branch, Head: head, Number: 12, Merged: true}
	wrong := merged
	wrong.Head = strings.Repeat("0", 40)
	if err := c.RemoveMerged(t.Context(), testLease(t), first, wrong); err == nil {
		t.Fatal("stale GitHub head removed the worktree")
	}
	dirty := filepath.Join(first.Path, "uncommitted.txt")
	if err := os.WriteFile(dirty, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveMerged(t.Context(), testLease(t), first, merged); err == nil {
		t.Fatal("merge cleanup discarded uncommitted work")
	}
	if text, _ := os.ReadFile(dirty); string(text) != "keep me" {
		t.Fatal("dirty work lost")
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	req.Task, req.Branch = "another", "task/another"
	second, err := c.Prepare(t.Context(), testLease(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveMerged(t.Context(), testLease(t), first, merged); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatalf("merged task retained: %v", err)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("other task removed: %v", err)
	}
	gitFixture(t, second.RepositoryPath, "fsck", "--full")
}

func TestCacheDoesNotInheritGitConfiguration(t *testing.T) {
	c, req, _ := fixture(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "never")
	t.Setenv("GIT_DIR", t.TempDir())
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	if _, err := c.Prepare(t.Context(), testLease(t), req); err != nil {
		t.Fatal(err)
	}
}
