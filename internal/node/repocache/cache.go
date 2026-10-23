// SPDX-License-Identifier: Apache-2.0

// Package repocache owns disposable repository clones and isolated task worktrees.
// Its caller owns task grants, GitHub observations and reconciliation. This
// package does not transfer commits to Core or infer a task result from Git.
package repocache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

type Repository struct{ Owner, Name string }

var repositoryPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// URL accepts GitHub identities only. Neither arbitrary remotes, Core paths nor
// credentials can enter the stored origin URL.
func (r Repository) URL() (string, error) {
	if !repositoryPart.MatchString(r.Owner) || !repositoryPart.MatchString(r.Name) || strings.HasSuffix(r.Name, ".git") {
		return "", errors.New("invalid GitHub repository identity")
	}
	return "https://github.com/" + strings.ToLower(r.Owner) + "/" + strings.ToLower(r.Name) + ".git", nil
}

type Request struct {
	Repository                  Repository
	Task, Attempt, Base, Branch string
}

type Checkout struct {
	Request              Request
	Path, RepositoryPath string
}

// MergedPullRequest is a trusted Core observation, not a worker receipt. The
// caller must read GitHub and bind it to the task before calling RemoveMerged.
type MergedPullRequest struct {
	Repository   Repository
	Branch, Head string
	Number       int
	Merged       bool
}

type Cache struct {
	root      string
	gitPath   string
	mu        sync.Mutex
	remote    func(Repository) string
	protocols string
	lockFile  *os.File
}

// Open creates no repository and performs no network operation.
func Open(root, gitPath string) (*Cache, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(gitPath) {
		return nil, errors.New("cache root and Git executable must be absolute")
	}
	if err := privateDir(root); err != nil {
		return nil, err
	}
	return &Cache{root: filepath.Clean(root), gitPath: gitPath, protocols: "https", remote: func(r Repository) string { url, _ := r.URL(); return url }}, nil
}

// Prepare fetches GitHub and creates a fresh task worktree. An existing attempt
// is preserved and refused: reconnect must reconcile it, never silently retry.
// A complete clone is renamed into place only after Git has finished.
func (c *Cache) Prepare(ctx context.Context, activity lease.Lease, req Request) (Checkout, error) {
	var checkout Checkout
	err := lease.Do(ctx, activity, func(ctx context.Context) error {
		var err error
		checkout, err = c.prepare(ctx, activity, req)
		return err
	})
	return checkout, err
}

func (c *Cache) prepare(ctx context.Context, activity lease.Lease, req Request) (Checkout, error) {
	if _, err := req.Repository.URL(); err != nil {
		return Checkout{}, err
	}
	for _, identity := range []string{req.Task, req.Attempt, req.Base, req.Branch} {
		if identity == "" || len(identity) > 512 || strings.ContainsAny(identity, "\x00\r\n") {
			return Checkout{}, errors.New("invalid task checkout identity")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Checkout{}, err
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return Checkout{}, err
	}
	defer unlock()
	for _, ref := range []string{req.Base, req.Branch} {
		if _, err := c.git(ctx, activity, c.root, "check-ref-format", "refs/heads/"+ref); err != nil {
			return Checkout{}, errors.New("invalid checkout ref")
		}
	}
	checkout := c.checkout(req)
	repoRoot := filepath.Dir(checkout.RepositoryPath)
	if err := privateDir(repoRoot); err != nil {
		return Checkout{}, err
	}
	if _, err := os.Lstat(checkout.Path); !errors.Is(err, os.ErrNotExist) {
		return Checkout{}, errors.New("task worktree already exists or cannot be inspected; reconcile it")
	}
	info, err := os.Lstat(checkout.RepositoryPath)
	if errors.Is(err, os.ErrNotExist) {
		stage, err := os.MkdirTemp(repoRoot, "clone-")
		if err != nil {
			return Checkout{}, err
		}
		defer os.RemoveAll(stage) // Only this invocation's unpublished clone.
		if _, err := c.git(ctx, activity, c.root, "clone", "--bare", "--no-hardlinks", "--", c.remote(req.Repository), stage); err != nil {
			return Checkout{}, err
		}
		if err := os.Rename(stage, checkout.RepositoryPath); err != nil {
			return Checkout{}, err
		}
	} else if err != nil {
		return Checkout{}, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Checkout{}, errors.New("cache repository is not a directory")
	}
	origin, err := c.git(ctx, activity, checkout.RepositoryPath, "remote", "get-url", "origin")
	if err != nil || origin != c.remote(req.Repository) {
		return Checkout{}, errors.New("cache origin does not match GitHub repository")
	}
	if _, err := c.git(ctx, activity, checkout.RepositoryPath, "fetch", "--no-tags", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return Checkout{}, err
	}
	if err := privateDir(filepath.Dir(checkout.Path)); err != nil {
		return Checkout{}, err
	}
	if _, err := c.git(ctx, activity, checkout.RepositoryPath, "worktree", "add", "-b", req.Branch, checkout.Path, "refs/remotes/origin/"+req.Base); err != nil {
		// Preserve a partial attempt for reconciliation. A failed command is not
		// evidence that no worktree or branch was created.
		return Checkout{}, err
	}
	return checkout, nil
}

func (c *Cache) checkout(req Request) Checkout {
	url, _ := req.Repository.URL()
	repoKey := fmt.Sprintf("%x", sha256.Sum256([]byte(url)))
	taskKey := fmt.Sprintf("%x", sha256.Sum256([]byte(req.Task+"\x00"+req.Attempt)))
	repoRoot := filepath.Join(c.root, repoKey)
	return Checkout{Request: req, RepositoryPath: filepath.Join(repoRoot, "repository.git"), Path: filepath.Join(repoRoot, "tasks", taskKey)}
}

// RemoveMerged removes only the requested clean worktree at the GitHub-observed
// head. It retains the repository and every other worktree. Unknown outcomes,
// stale observations and uncommitted files require the caller to reconcile.
func (c *Cache) RemoveMerged(ctx context.Context, activity lease.Lease, checkout Checkout, merged MergedPullRequest) error {
	return lease.Do(ctx, activity, func(ctx context.Context) error {
		return c.removeMerged(ctx, activity, checkout, merged)
	})
}

func (c *Cache) removeMerged(ctx context.Context, activity lease.Lease, checkout Checkout, merged MergedPullRequest) error {
	url, err := checkout.Request.Repository.URL()
	if err != nil {
		return err
	}
	mergedURL, err := merged.Repository.URL()
	if err != nil || url != mergedURL || !merged.Merged || merged.Number < 1 || merged.Branch != checkout.Request.Branch {
		return errors.New("merged pull request does not match task")
	}
	if checkout != c.checkout(checkout.Request) {
		return errors.New("checkout is outside the task cache")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	info, err := os.Lstat(checkout.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("task checkout is not a directory")
	}
	head, err := c.git(ctx, activity, checkout.Path, "rev-parse", "HEAD")
	if err != nil || head != merged.Head {
		return errors.New("task head differs from merged pull request")
	}
	branch, err := c.git(ctx, activity, checkout.Path, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != merged.Branch {
		return errors.New("task branch differs from merged pull request")
	}
	_, err = c.git(ctx, activity, checkout.RepositoryPath, "worktree", "remove", "--", checkout.Path)
	return err
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("cache directory must be private and not a symlink")
	}
	return nil
}

func (c *Cache) git(ctx context.Context, activity lease.Lease, dir string, args ...string) (string, error) {
	options := []string{"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null", "-c", "http.followRedirects=false", "-c", "submodule.recurse=false"}
	var out bytes.Buffer
	cmd := proc.Command{Path: c.gitPath, Args: append(options, args...), Dir: dir, Stdout: &out}
	// The Git child holds the same kernel lock if the Node dies while Git is
	// still running. A replacement Node cannot mutate its repository meanwhile.
	if c.lockFile != nil {
		cmd.ExtraFiles = []*os.File{c.lockFile}
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + c.root,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false", "GIT_ALLOW_PROTOCOL=" + c.protocols}
	owner := proc.NewOwner()
	defer owner.Close(context.WithoutCancel(ctx))
	err := owner.Run(ctx, activity, cmd)
	if err != nil {
		// Git diagnostics can contain remote-controlled bytes. Never retain them
		// in operational state or return them as trusted task evidence.
		return "", fmt.Errorf("repository cache git operation failed: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}
