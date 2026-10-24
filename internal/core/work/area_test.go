// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"fmt"
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
	return areaFixtureFormat(t, "sha1")
}
func areaFixtureFormat(t *testing.T, format string) (*Area, WorkspaceSpec, string) {
	t.Helper()
	source := t.TempDir()
	areaGit(t, source, "init", "--object-format="+format, "-b", "main")
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
func TestAreaRegisteredWorktreeUsesConfiguredGit(t *testing.T) {
	t.Parallel()
	a, spec, source := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	dir := filepath.Join(a.path, "tasks", "task-1")
	remote := t.TempDir()
	areaGit(t, source, "init", "--bare", remote)
	areaGit(t, source, "remote", "add", "origin", remote)
	areaGit(t, source, "worktree", "add", "-b", spec.Branch, dir, spec.Base)
	git := a.git
	a.git = "" // Core may boot without Git on PATH; delegation config supplies it.
	if err := a.ProtectLinkedWorktree(t.Context(), l, git, "task-1", source, spec.Branch); err != nil {
		t.Fatal(err)
	}
	areaGit(t, dir, "commit", "--allow-empty", "-m", "own assigned branch")
	push := exec.Command(git, "-C", dir, "push", areaGit(t, dir, "remote", "get-url", "origin"), "HEAD:refs/heads/"+spec.Branch)
	push.Env = []string{"PATH=", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("assigned branch push: %v %s", err, out)
	}
	head := areaGit(t, dir, "rev-parse", "HEAD")
	if got := areaGit(t, remote, "rev-parse", spec.Branch); got != head {
		t.Fatalf("assigned branch was not published: %s", got)
	}
	command := exec.Command(git, "-C", dir, "update-ref", "refs/heads/main", head)
	if out, err := command.CombinedOutput(); err == nil || !strings.Contains(string(out), "LINA protected ref") {
		t.Fatalf("registered worktree changed protected ref: %v %s", err, out)
	}
}

// A worker pushes its own assigned branch with a normal `git push`. Git may
// update the remote-tracking ref that mirrors that branch, by remote name and,
// from 2.56, by a URL that matches one configured remote.
func TestAreaRegisteredWorktreePushesAssignedBranch(t *testing.T) {
	t.Parallel()
	a, spec, source := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	dir := filepath.Join(a.path, "tasks", "task-1")
	remote := t.TempDir()
	areaGit(t, source, "init", "--bare", remote)
	areaGit(t, source, "remote", "add", "origin", remote)
	areaGit(t, source, "worktree", "add", "-b", spec.Branch, dir, spec.Base)
	if err := a.ProtectLinkedWorktree(t.Context(), l, a.git, "task-1", source, spec.Branch); err != nil {
		t.Fatal(err)
	}
	areaGit(t, dir, "commit", "--allow-empty", "-m", "own assigned branch")
	head := areaGit(t, dir, "rev-parse", "HEAD")
	url := areaGit(t, dir, "remote", "get-url", "origin")
	for _, target := range []string{"origin", url} {
		areaGit(t, dir, "push", target, "HEAD:refs/heads/"+spec.Branch)
		if got := areaGit(t, remote, "rev-parse", spec.Branch); got != head {
			t.Fatalf("push to %s did not publish the assigned branch: %s", target, got)
		}
	}
	if got := areaGit(t, dir, "rev-parse", "--verify", "refs/remotes/origin/"+spec.Branch); got != head {
		t.Fatalf("own branch mirror not updated: %s", got)
	}
}

// The mirror allowance covers only the task's own branch name.
func TestAreaRegisteredWorktreeRejectsOtherRemoteTrackingRefs(t *testing.T) {
	t.Parallel()
	a, spec, source := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	dir := filepath.Join(a.path, "tasks", "task-1")
	areaGit(t, source, "worktree", "add", "-b", spec.Branch, dir, spec.Base)
	if err := a.ProtectLinkedWorktree(t.Context(), l, a.git, "task-1", source, spec.Branch); err != nil {
		t.Fatal(err)
	}
	areaGit(t, dir, "commit", "--allow-empty", "-m", "own assigned branch")
	head := areaGit(t, dir, "rev-parse", "HEAD")
	for _, ref := range []string{"refs/remotes/origin/task/task-2", "refs/remotes/origin/dev"} {
		c := exec.Command("git", "update-ref", ref, head)
		c.Dir = dir
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if out, err := c.CombinedOutput(); err == nil || !strings.Contains(string(out), "LINA protected ref") {
			t.Fatalf("worker changed %s: %v %s", ref, err, out)
		}
	}
}

// The guard of an earlier Core still protects every ref, so an upgrade keeps
// serving that clone and replaces the guard in place.
const previousRefGuard = `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
[ "$1" = prepared ] || exit 0
assigned=
binding=$(git rev-parse --git-path lina-task-ref) || exit 1
if [ -f "$binding" ]; then
 IFS= read -r assigned < "$binding" || exit 1
fi
while read -r old new ref; do
 case "$ref" in
 refs/heads/main|refs/heads/master|refs/heads/dev|refs/remotes/*|refs/lina/*)
  echo 'LINA protected ref' >&2
  exit 1;;
 esac
done
`

func TestAreaRegisteredWorktreeReplacesEarlierGuard(t *testing.T) {
	t.Parallel()
	a, spec, source := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	areaGit(t, source, "worktree", "add", "-b", spec.Branch, filepath.Join(a.path, "tasks", "task-1"), spec.Base)
	if err := a.ProtectLinkedWorktree(t.Context(), l, a.git, "task-1", source, spec.Branch); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(source, ".git", "lina-hooks", "reference-transaction")
	if err := os.WriteFile(guard, []byte(previousRefGuard), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.ProtectLinkedWorktree(t.Context(), l, a.git, "task-1", source, spec.Branch); err != nil {
		t.Fatalf("refused an earlier Core guard: %v", err)
	}
	if body, err := os.ReadFile(guard); err != nil || string(body) != refGuard {
		t.Fatalf("kept the earlier guard: %v", err)
	}
	// A hook this package did not write is still refused, not replaced.
	if err := os.WriteFile(guard, []byte("#!/bin/sh\necho 'LINA protected ref'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.ProtectLinkedWorktree(t.Context(), l, a.git, "task-1", source, spec.Branch); err == nil {
		t.Fatal("accepted a foreign hook")
	}
}

func TestAreaRegisteredWorktreePreservesUnrelatedHooks(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "configured"}[explicit], func(t *testing.T) {
			a, spec, source := areaFixture(t)
			l := lease.New(func() {})
			defer l.Release()
			areaGit(t, source, "worktree", "add", "-b", spec.Branch, filepath.Join(a.path, "tasks", "task-1"), spec.Base)
			hooks := filepath.Join(source, ".git", "hooks")
			configured := ""
			if explicit {
				hooks = t.TempDir()
				configured = hooks
				areaGit(t, source, "config", "core.hooksPath", hooks)
			}
			path := filepath.Join(hooks, "pre-commit")
			const existing = "#!/bin/sh\nexit 23\n"
			if err := os.WriteFile(path, []byte(existing), 0700); err != nil {
				t.Fatal(err)
			}
			if err := a.ProtectLinkedWorktree(t.Context(), l, a.git, "task-1", source, spec.Branch); err == nil {
				t.Fatal("replaced unrelated clone hooks")
			}
			if got := areaGit(t, source, "config", "--local", "--get", "--default", "", "core.hooksPath"); got != configured {
				t.Fatalf("changed hooksPath: %q", got)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != existing {
				t.Fatalf("changed existing hook: %q %v", got, err)
			}
		})
	}
}

func TestAreaPinnedWorktreeAndProtectedRefs(t *testing.T) {
	t.Parallel()
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
func TestAreaSourceIdentitySurvivesWorkerRemote(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			a, spec, source := areaFixture(t)
			l := lease.New(func() {})
			defer l.Release()
			w, err := a.Prepare(t.Context(), l, "task-1", spec)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				areaGit(t, w.Path, "config", "--unset", "lina.source")
				areaGit(t, w.Path, "switch", "-c", spec.Branch)
				areaGit(t, w.Path, "commit", "--allow-empty", "-m", "worker progress")
				head := areaGit(t, w.Path, "rev-parse", "HEAD")
				if _, err = a.Prepare(t.Context(), l, "task-1", spec); err != nil {
					t.Fatal(err)
				}
				if areaGit(t, w.Path, "rev-parse", "HEAD") != head {
					t.Fatal("source migration reset worker progress")
				}
			}
			areaGit(t, w.Path, "remote", "set-url", "origin", "https://github.com/fixture/project.git")
			// A worker's publishing remote must not replace the allocator's local source.
			if _, err = a.Prepare(t.Context(), l, "task-1", spec); err != nil {
				t.Fatalf("publishing remote lost allocation source: %v", err)
			}
			areaGit(t, source, "commit", "--allow-empty", "-m", "new source commit")
			spec.Base = areaGit(t, source, "rev-parse", "HEAD")
			spec.Branch = "task/task-2"
			if _, err = a.Prepare(t.Context(), l, "task-2", spec); err != nil {
				t.Fatal(err)
			}
			spec.Source = t.TempDir()
			if _, err = a.Prepare(t.Context(), l, "task-2", spec); err == nil {
				t.Fatal("different local source accepted")
			}
		})
	}
}

func TestAreaRefusesEscapesAndConflictingAllocation(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestAreaTaskBranchOwnership(t *testing.T) {
	t.Parallel()
	a, spec, _ := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	first, err := a.Prepare(t.Context(), l, "task-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Branch = "task/task-2"
	second, err := a.Prepare(t.Context(), l, "task-2", spec)
	if err != nil {
		t.Fatal(err)
	}
	// Both allocations are detached; protection must precede branch creation.
	reject := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		out, err := c.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "LINA protected ref") {
			t.Errorf("git %v did not reject protected ref: %v %s", args, err, out)
		}
	}
	firstRef := "refs/heads/" + first.Spec.Branch
	secondRef := "refs/heads/" + second.Spec.Branch
	reject(second.Path, "update-ref", firstRef, spec.Base)
	// Restore the fixture even on the unfixed implementation, so all ordinary
	// update/delete paths produce independent regression evidence.
	if _, err = a.command(t.Context(), l, first.Path, "update-ref", "-d", firstRef); err != nil {
		t.Fatal(err)
	}
	for _, w := range []Workspace{first, second} {
		areaGit(t, w.Path, "switch", "-c", w.Spec.Branch)
		areaGit(t, w.Path, "commit", "--allow-empty", "-m", w.TaskID)
	}
	firstHead := areaGit(t, first.Path, "rev-parse", "HEAD")
	secondHead := areaGit(t, second.Path, "rev-parse", "HEAD")
	for _, args := range [][]string{
		{"update-ref", firstRef, secondHead},
		{"update-ref", "-d", firstRef},
		{"branch", "--force", first.Spec.Branch, secondHead},
	} {
		// branch --force normally checks checked-out branches; detach the owner
		// to prove the ownership guard, rather than Git's checkout check.
		areaGit(t, first.Path, "switch", "--detach", firstHead)
		reject(second.Path, args...)
		if got := areaGit(t, first.Path, "show-ref", "--verify", "--hash", firstRef); got != firstHead {
			t.Errorf("other task branch moved: %s", got)
		}
		if _, err = a.command(t.Context(), l, first.Path, "update-ref", firstRef, firstHead); err != nil {
			t.Fatal(err)
		}
	}
	reject(first.Path, "update-ref", secondRef, firstHead)
	c := exec.Command("git", "update-ref", "--stdin")
	c.Dir = second.Path
	c.Stdin = strings.NewReader("start\nupdate " + secondRef + " " + firstHead + "\nupdate " + firstRef + " " + secondHead + "\nprepare\ncommit\n")
	if out, err := c.CombinedOutput(); err == nil || !strings.Contains(string(out), "LINA protected ref") {
		t.Fatalf("mixed transaction was not rejected: %v %s", err, out)
	}
	if areaGit(t, first.Path, "rev-parse", firstRef) != firstHead || areaGit(t, second.Path, "rev-parse", secondRef) != secondHead {
		t.Fatal("rejected transaction partially changed task branches")
	}
	// Assignment is independent of HEAD and survives reopening Core's area.
	reopened, err := OpenArea(filepath.Dir(a.path), a.git)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	a = reopened
	areaGit(t, first.Path, "switch", first.Spec.Branch)
	areaGit(t, first.Path, "commit", "--allow-empty", "-m", "owner continues")
	if _, err = a.Prepare(t.Context(), l, first.TaskID, first.Spec); err != nil {
		t.Fatal(err)
	}
	head := areaGit(t, first.Path, "rev-parse", "HEAD")
	bundle, err := a.Bundle(t.Context(), l, first, head)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(bundle)
	if got := areaGit(t, first.Path, "bundle", "list-heads", bundle); got != head+" "+firstRef {
		t.Fatalf("retained wrong branch: %s", got)
	}
	for _, w := range []Workspace{first, second} {
		if got := areaGit(t, w.Path, "rev-parse", "refs/lina/base/"+w.TaskID); got != w.Spec.Base {
			t.Fatalf("lost pinned base: %s", got)
		}
	}
}

func TestAreaRefusesConflictingBranchOwnership(t *testing.T) {
	t.Parallel()
	a, spec, _ := areaFixture(t)
	l := lease.New(func() {})
	defer l.Release()
	spec.Branch = "task/task-1-task-2"
	first, err := a.Prepare(t.Context(), l, "task-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Prepare(t.Context(), l, "task-2", spec); err == nil {
		t.Fatal("two tasks reserved the same uncreated branch")
	}
	spec.Branch = "task/other-task-1"
	if _, err = a.Prepare(t.Context(), l, "task-1", spec); err == nil {
		t.Fatal("retry reassigned an existing worktree's branch")
	}
	areaGit(t, first.Path, "switch", "-c", first.Spec.Branch)
	areaGit(t, first.Path, "commit", "--allow-empty", "-m", "original owner")
	// A failed allocation can retry its own non-conflicting branch.
	spec.Branch = "task/task-2"
	second, err := a.Prepare(t.Context(), l, "task-2", spec)
	if err != nil {
		t.Fatal(err)
	}
	areaGit(t, second.Path, "switch", "-c", spec.Branch)
	areaGit(t, second.Path, "commit", "--allow-empty", "-m", "second owner")
}

func TestAreaRefusesTaskBranchRename(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, flag, target string
		detached, implicit bool
		peerExists         bool
	}{
		{name: "reserved", flag: "-m", target: "task/task-2"},
		{name: "current", flag: "-m", target: "task/task-2", implicit: true},
		{name: "detached", flag: "-m", target: "task/task-2", detached: true},
		{name: "unassigned", flag: "--move", target: "task/renamed"},
		{name: "force-reserved", flag: "-M", target: "task/task-2"},
		{name: "force-existing", flag: "-M", target: "task/task-2", peerExists: true},
		{name: "force-protected", flag: "-M", target: "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, spec, source := areaFixture(t)
			l := lease.New(func() {})
			defer l.Release()
			first, err := a.Prepare(t.Context(), l, "task-1", spec)
			if err != nil {
				t.Fatal(err)
			}
			spec.Branch = "task/task-2"
			second, err := a.Prepare(t.Context(), l, "task-2", spec)
			if err != nil {
				t.Fatal(err)
			}
			areaGit(t, first.Path, "switch", "-c", first.Spec.Branch)
			areaGit(t, first.Path, "commit", "--allow-empty", "-m", "original result")
			head := areaGit(t, first.Path, "rev-parse", "HEAD")
			if tc.detached {
				areaGit(t, first.Path, "switch", "--detach")
			}
			if tc.peerExists {
				areaGit(t, second.Path, "switch", "-c", second.Spec.Branch)
				areaGit(t, second.Path, "commit", "--allow-empty", "-m", "peer result")
				// Remove Git's checked-out-branch protection from the experiment.
				areaGit(t, second.Path, "switch", "--detach")
			}
			refs := areaGit(t, first.Path, "show-ref")
			headRef := areaGit(t, first.Path, "rev-parse", "--abbrev-ref", "HEAD")
			sourceRefs := areaGit(t, source, "show-ref")
			args := []string{"branch", tc.flag}
			if !tc.implicit {
				args = append(args, first.Spec.Branch)
			}
			args = append(args, tc.target)
			c := exec.Command("git", args...)
			c.Dir = first.Path
			c.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
			out, err := c.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "LINA protected ref") {
				t.Fatalf("git %v did not reject rename: %v %s", args, err, out)
			}
			if got := areaGit(t, first.Path, "show-ref"); got != refs {
				t.Fatalf("rejected rename changed refs:\nbefore: %s\nafter: %s", refs, got)
			}
			if areaGit(t, first.Path, "rev-parse", "--abbrev-ref", "HEAD") != headRef || areaGit(t, first.Path, "rev-parse", "HEAD") != head {
				t.Fatal("rejected rename changed worker HEAD")
			}
			if areaGit(t, source, "show-ref") != sourceRefs {
				t.Fatal("rename changed source repository")
			}
			bundle, err := a.Bundle(t.Context(), l, first, head)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(bundle)
			if got := areaGit(t, first.Path, "bundle", "list-heads", bundle); got != head+" refs/heads/"+first.Spec.Branch {
				t.Fatalf("lost original result: %s", got)
			}
			areaGit(t, first.Path, "switch", first.Spec.Branch)
			areaGit(t, first.Path, "commit", "--allow-empty", "-m", "owner continues")
			if !tc.peerExists {
				areaGit(t, second.Path, "switch", "-c", second.Spec.Branch)
			} else {
				areaGit(t, second.Path, "switch", second.Spec.Branch)
			}
			areaGit(t, second.Path, "commit", "--allow-empty", "-m", "peer continues")
		})
	}
}

func TestAreaAssignedBranchDeletionRequiresCore(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			a, spec, _ := areaFixtureFormat(t, format)
			l := lease.New(func() {})
			defer l.Release()
			w, err := a.Prepare(t.Context(), l, "task-1", spec)
			if err != nil {
				t.Fatal(err)
			}
			areaGit(t, w.Path, "switch", "-c", spec.Branch)
			areaGit(t, w.Path, "commit", "--allow-empty", "-m", "result")
			head := areaGit(t, w.Path, "rev-parse", "HEAD")
			areaGit(t, w.Path, "switch", "--detach")
			ref := "refs/heads/" + spec.Branch
			for _, args := range [][]string{
				{"update-ref", "-d", ref},
				{"branch", "-D", spec.Branch},
			} {
				c := exec.Command("git", args...)
				c.Dir = w.Path
				c.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
				if out, err := c.CombinedOutput(); err == nil || !strings.Contains(string(out), "LINA protected ref") {
					t.Fatalf("git %v did not reject assigned branch deletion: %v %s", args, err, out)
				}
				if areaGit(t, w.Path, "rev-parse", ref) != head {
					t.Fatal("rejected deletion lost the result head")
				}
			}
			// Only Core's explicit administrative path may clean up the branch.
			if _, err := a.command(t.Context(), l, w.Path, "update-ref", "-d", ref); err != nil {
				t.Fatal(err)
			}
			if got := areaGit(t, w.Path, "for-each-ref", "--format=%(refname)", ref); got != "" {
				t.Fatalf("Core did not remove the assigned branch: %s", got)
			}
		})
	}
}

func TestAreaRefusesReplacedHookWithoutOverwritingTarget(t *testing.T) {
	t.Parallel()
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
