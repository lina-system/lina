// SPDX-License-Identifier: Apache-2.0
package work

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

// WorkspaceSpec pins a local source repository and a task-owned branch. Empty
// repository fields request a non-Git directory. Source is never modified.
type WorkspaceSpec struct {
	RepositoryID string `json:"repository_id,omitempty"`
	Source       string `json:"source,omitempty"`
	Base         string `json:"base,omitempty"`
	Branch       string `json:"branch,omitempty"`
}
type Workspace struct {
	TaskID string        `json:"task_id"`
	Path   string        `json:"path"`
	Spec   WorkspaceSpec `json:"spec"`
}
type Area struct {
	mu        sync.Mutex
	path, git string
	root      *os.Root
	owner     *proc.Owner
}

func OpenArea(stateRoot, git string) (*Area, error) {
	if !filepath.IsAbs(stateRoot) || (git != "" && !filepath.IsAbs(git)) {
		return nil, ErrInvalid
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = privateAreaDir(root, "work"); err != nil {
		return nil, err
	}
	workRoot, err := root.OpenRoot("work")
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{"clones", "tasks", "runs"} {
		if err = privateAreaDir(workRoot, dir); err != nil {
			workRoot.Close()
			return nil, err
		}
	}
	return &Area{path: filepath.Join(stateRoot, "work"), git: git, root: workRoot, owner: proc.NewOwner()}, nil
}
func (a *Area) Close(ctx context.Context) error {
	return errors.Join(a.owner.Close(ctx), a.root.Close())
}
func privateAreaDir(root *os.Root, path string) error {
	err := root.Mkdir(path, 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return ErrInvalid
	}
	return nil
}
func areaID(s string) bool {
	if len(s) == 0 || len(s) > 128 || s[0] == '-' || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
func commitID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && (len(b) == 20 || len(b) == 32) && hex.EncodeToString(b) == s
}
func protectedBranch(s string) bool { return s == "main" || s == "master" || s == "dev" }
func (s WorkspaceSpec) validate(id string) error {
	if !areaID(id) {
		return ErrInvalid
	}
	if s.RepositoryID == "" {
		if s.Source != "" || s.Base != "" || s.Branch != "" {
			return ErrInvalid
		}
		return nil
	}
	if !areaID(s.RepositoryID) || !absolute(s.Source) || !commitID(s.Base) || protectedBranch(s.Branch) || !strings.Contains(s.Branch, id) || strings.HasPrefix(s.Branch, "-") {
		return ErrInvalid
	}
	return nil
}

// Core commands use fixed arguments and an explicit environment. Source
// repository hooks, user Git aliases and injected GIT_* settings never run.
func (a *Area) command(ctx context.Context, l lease.Lease, dir string, args ...string) (string, error) {
	if a.git == "" {
		return "", ErrInvalid
	}
	var out, stderr bytes.Buffer
	env := []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always"}, args...)
	err := a.owner.Run(ctx, l, proc.Command{Path: a.git, Dir: dir, Env: env, Args: args, Stdout: &out, Stderr: &stderr})
	if err != nil {
		return "", fmt.Errorf("work git: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

// A worker is unconfined. This guard prevents ordinary Git commands from
// changing the parent clone's protected refs; it is not an OS security fence.
const refGuard = `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
[ "$1" = prepared ] || exit 0
while read -r old new ref; do
 case "$ref" in
 refs/heads/main|refs/heads/master|refs/heads/dev|refs/remotes/*|refs/lina/*)
  echo 'LINA protected ref' >&2
  exit 1;;
 esac
done
`

func (a *Area) Prepare(ctx context.Context, l lease.Lease, id string, s WorkspaceSpec) (Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := Workspace{TaskID: id, Path: filepath.Join(a.path, "tasks", id), Spec: s}
	if err := s.validate(id); err != nil {
		return Workspace{}, err
	}
	if s.RepositoryID == "" {
		return w, privateAreaDir(a.root, filepath.Join("tasks", id))
	}
	if _, err := a.command(ctx, l, a.path, "check-ref-format", "refs/heads/"+s.Branch); err != nil {
		return Workspace{}, ErrInvalid
	}
	clone := filepath.Join(a.path, "clones", s.RepositoryID)
	rel := filepath.Join("clones", s.RepositoryID)
	if info, err := a.root.Lstat(rel); errors.Is(err, os.ErrNotExist) {
		// --no-local avoids shared objects and source hooks/configuration. Rename
		// publishes a complete clone; failures leave no apparently usable clone.
		temp, err := os.MkdirTemp(filepath.Join(a.path, "clones"), ".clone-")
		if err != nil {
			return Workspace{}, err
		}
		defer os.RemoveAll(temp)
		if _, err = a.command(ctx, l, a.path, "clone", "--bare", "--no-local", "--", s.Source, temp); err != nil {
			return Workspace{}, err
		}
		if err = os.Chmod(filepath.Join(temp, "hooks"), 0700); err != nil {
			return Workspace{}, err
		}
		if err = os.Rename(temp, clone); err != nil {
			return Workspace{}, err
		}
	} else if err != nil {
		return Workspace{}, err
	} else if !info.IsDir() || info.Mode().Perm() != 0700 {
		return Workspace{}, ErrInvalid
	}
	origin, err := a.command(ctx, l, clone, "config", "--get", "remote.origin.url")
	if err != nil || origin != s.Source {
		return Workspace{}, ErrState
	}
	base, err := a.command(ctx, l, clone, "rev-parse", "--verify", s.Base+"^{commit}")
	if err != nil {
		// A later task can name a new source commit without moving any existing
		// branch, tracking ref or task checkout.
		if _, err = a.command(ctx, l, clone, "fetch", "--no-tags", "--no-write-fetch-head", "--", s.Source, s.Base); err != nil {
			return Workspace{}, err
		}
		base, err = a.command(ctx, l, clone, "rev-parse", "--verify", s.Base+"^{commit}")
	}
	if err != nil || base != s.Base {
		return Workspace{}, ErrInvalid
	}
	hookPath := filepath.Join(clone, "hooks")
	if err = privateAreaDir(a.root, filepath.Join(rel, "hooks")); err != nil {
		return Workspace{}, err
	}
	if err = a.installGuard(filepath.Join(rel, "hooks")); err != nil {
		return Workspace{}, err
	}
	if _, err = a.command(ctx, l, clone, "config", "core.hooksPath", hookPath); err != nil {
		return Workspace{}, err
	}
	ref := "refs/lina/base/" + id
	if old, e := a.command(ctx, l, clone, "rev-parse", "--verify", ref); e == nil {
		if old != s.Base {
			return Workspace{}, ErrState
		}
	} else {
		if _, err = a.command(ctx, l, clone, "update-ref", ref, s.Base, strings.Repeat("0", len(s.Base))); err != nil {
			return Workspace{}, err
		}
	}
	if info, err := a.root.Lstat(filepath.Join("tasks", id)); err == nil {
		if !info.IsDir() {
			return Workspace{}, ErrInvalid
		}
		common, err := a.command(ctx, l, w.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || filepath.Clean(common) != clone {
			return Workspace{}, ErrState
		}
		return w, nil // Never reset or clean existing task work.
	} else if !errors.Is(err, os.ErrNotExist) {
		return Workspace{}, err
	}
	if _, err = a.command(ctx, l, clone, "worktree", "add", "--detach", "--", w.Path, s.Base); err != nil {
		return Workspace{}, err
	}
	if err = os.Chmod(w.Path, 0700); err != nil {
		return Workspace{}, err
	}
	return w, nil
}

// Bundle captures the exact named branch, including its complete object history.
// The caller owns the temporary file and must remove it after materialization.
func (a *Area) Bundle(ctx context.Context, l lease.Lease, w Workspace, head string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if w.Spec.validate(w.TaskID) != nil || w.Spec.RepositoryID == "" || w.Path != filepath.Join(a.path, "tasks", w.TaskID) || !commitID(head) {
		return "", ErrInvalid
	}
	actual, err := a.command(ctx, l, w.Path, "rev-parse", "--verify", "refs/heads/"+w.Spec.Branch)
	if err != nil || actual != head {
		return "", ErrState
	}
	f, err := os.CreateTemp(a.path, ".bundle-")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err = f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if _, err = a.command(ctx, l, w.Path, "bundle", "create", path, "refs/heads/"+w.Spec.Branch); err == nil {
		var listed string
		listed, err = a.command(ctx, l, w.Path, "bundle", "list-heads", path)
		if err == nil && listed != head+" refs/heads/"+w.Spec.Branch {
			err = ErrState
		}
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// Publish the guard by rename inside the open work root. A substituted link
// must never turn allocation into a write to an unrelated user file.
func (a *Area) installGuard(dir string) (err error) {
	path := filepath.Join(dir, "reference-transaction")
	if info, e := a.root.Lstat(path); e == nil {
		if !info.Mode().IsRegular() {
			return ErrInvalid
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	temp := filepath.Join(dir, ".reference-"+rand.Text())
	f, err := a.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	defer a.root.Remove(temp)
	_, err = f.WriteString(refGuard)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return a.root.Rename(temp, path)
}
