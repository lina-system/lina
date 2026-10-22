// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

func findProgram(env []string, name string) string {
	for _, entry := range env {
		if path, ok := strings.CutPrefix(entry, "PATH="); ok {
			for _, dir := range filepath.SplitList(path) {
				if !filepath.IsAbs(dir) {
					continue
				}
				p := filepath.Join(dir, name)
				if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
					return p
				}
			}
		}
	}
	return ""
}

// ProtectedRefs reads the parent clone, not a remote-tracking ref. It checks the
// origin binding and does not fetch, update refs, check out, or execute a shell.
func (c *Client) ProtectedRefs(ctx context.Context, clone, repository string, names []string) (map[string]string, error) {
	if !filepath.IsAbs(clone) || len(names) == 0 || len(names) > 64 {
		return nil, errors.New("invalid_protected_refs")
	}
	ref, err := normalize(Ref{repository, 1})
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		branch, ok := strings.CutPrefix(name, "refs/heads/")
		if !ok || !validBranch(branch) {
			return nil, errors.New("invalid_protected_refs")
		}
	}
	ctx, done, err := c.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	env := proc.Environment(c.cfg.Env, "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS")
	path := findProgram(env, "git")
	if path == "" {
		return nil, errors.New("git_unavailable")
	}
	l, err := c.cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "Protected Git ref check"})
	if err != nil {
		return nil, err
	}
	defer l.Release()
	read := func(args ...string) (string, error) {
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		var out bounded
		err := c.runner.Run(deadline, l, proc.Command{Path: path, Args: append([]string{"--no-optional-locks", "-C", clone}, args...), Env: env, Stdout: &out, Stderr: io.Discard})
		if err != nil || out.overflow {
			return "", errors.New("protected_ref_unreadable")
		}
		return strings.TrimSpace(out.String()), nil
	}
	origin, err := read("remote", "get-url", "origin")
	if err != nil {
		return nil, err
	}
	origin = strings.TrimSuffix(strings.TrimSuffix(origin, "/"), ".git")
	allowed := false
	for _, prefix := range []string{"https://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		if value, ok := strings.CutPrefix(origin, prefix); ok && strings.EqualFold(value, ref.Repository) {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("clone_repository_mismatch")
	}
	refs := map[string]string{}
	for _, name := range names {
		sha, err := read("show-ref", "--verify", "--hash", name)
		if err != nil {
			return nil, err
		}
		if !shaPattern.MatchString(sha) {
			return nil, errors.New("protected_ref_unreadable")
		}
		refs[name] = sha
	}
	return refs, nil
}
