// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/kit/lease"
)

// ProtectLinkedWorktree applies the same guard and ownership binding as Prepare
// to a scheduler-created worktree of an installation-registered clone. It keeps
// that clone's remote and refs intact and refuses to replace unrelated hooks.
// Git is the installation's absolute executable, independent of Core's PATH.
func (a *Area) ProtectLinkedWorktree(ctx context.Context, l lease.Lease, git, id, clone, branch string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !absolute(git) || !areaID(id) || !absolute(clone) || protectedBranch(branch) || !strings.Contains(branch, id) {
		return ErrInvalid
	}
	if _, err := a.commandWithGit(ctx, l, git, clone, "check-ref-format", "refs/heads/"+branch); err != nil {
		return ErrInvalid
	}
	common, err := a.commandWithGit(ctx, l, git, clone, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	workdir := filepath.Join(a.path, "tasks", id)
	actual, err := a.commandWithGit(ctx, l, git, workdir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(actual) != filepath.Clean(common) {
		return ErrState
	}
	gitdir, err := a.commandWithGit(ctx, l, git, workdir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(common, gitdir)
	if err != nil || filepath.Dir(rel) != "worktrees" {
		return ErrState
	}
	root, err := os.OpenRoot(common)
	if err != nil {
		return err
	}
	defer root.Close()
	// --local reads the stored setting, not command's administrative override.
	configured, err := a.commandWithGit(ctx, l, git, clone, "config", "--local", "--get", "--default", "", "core.hooksPath")
	if err != nil {
		return err
	}
	hooks := "lina-hooks"
	if configured == "" {
		// Git also runs executable hooks in the default directory without a
		// hooksPath setting. Do not silently disable installation-owned hooks.
		dir, err := root.Open("hooks")
		if err == nil {
			entries, readErr := dir.ReadDir(-1)
			if err = errors.Join(readErr, dir.Close()); err != nil {
				return err
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".sample") {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 != 0 && !info.IsDir() {
					return ErrState
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if configured != "" && configured != filepath.Join(common, hooks) {
		// A clone already managed by Prepare has this same guard in hooks/.
		body, e := root.ReadFile(filepath.Join("hooks", "reference-transaction"))
		if configured != filepath.Join(common, "hooks") || e != nil || !linaGuard(body) {
			return ErrState
		}
		hooks = "hooks"
	}
	guard := filepath.Join(hooks, "reference-transaction")
	if body, err := root.ReadFile(guard); err == nil {
		if !linaGuard(body) {
			return ErrState
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = privateAreaDir(root, hooks); err != nil {
		return err
	}
	if err = writeAreaFile(root, guard, refGuard, 0700); err != nil {
		return err
	}
	if err = bindTaskRef(root, rel, "worktrees", branch); err != nil {
		return err
	}
	_, err = a.commandWithGit(ctx, l, git, clone, "config", "--local", "core.hooksPath", filepath.Join(common, hooks))
	return err
}
