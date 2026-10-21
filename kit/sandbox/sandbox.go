// SPDX-License-Identifier: Apache-2.0

// Package sandbox invokes the host OS sandbox without an unsandboxed fallback.
// Core supplies paths and authorized scopes; this package knows no state layout.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

const PolicyVersion = "lina-sandbox-1"

var ErrUnavailable = errors.New("sandbox unavailable")

type Status struct {
	Available bool
	Cause     string
}

// Config is trusted host configuration, never model-supplied tool arguments.
// WriteRoots contains only authorized grant roots. ReadDenied always wins.
type Config struct {
	WorkDir                string
	WriteRoots, ReadDenied []string
	Env                    []string
	ProviderVariables      []string
}
type Command struct {
	Path string
	Args []string
	// Network is computed by Core's approval gate or installed service profile.
	Network        bool
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}
type Sandbox struct {
	owner     *proc.Owner
	config    Config
	mu        sync.Mutex
	status    Status
	firstCall bool
}

func New(owner *proc.Owner, c Config) *Sandbox {
	c.WriteRoots = append([]string{}, c.WriteRoots...)
	c.ReadDenied = append([]string{}, c.ReadDenied...)
	c.ProviderVariables = append([]string{}, c.ProviderVariables...)
	if c.Env == nil {
		c.Env = proc.InheritedEnvironment(c.ProviderVariables...)
	} else {
		c.Env = proc.Environment(c.Env, c.ProviderVariables...)
	}
	return &Sandbox{owner: owner, config: c, status: Status{Cause: "not_probed"}}
}
func (s *Sandbox) Status() Status { s.mu.Lock(); defer s.mu.Unlock(); return s.status }

// Probe is bounded and never makes Core startup fail. It tests both execution
// and a newly planted denied read using the same policy builder as real calls.
func (s *Sandbox) Probe(ctx context.Context, l lease.Lease) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, err := s.probe(ctx, l)
	if err != nil {
		if errors.Is(err, lease.ErrInvalid) || errors.Is(err, lease.ErrReleased) {
			return Status{Cause: "invalid_lease"}
		}
		return Status{Cause: "probe_cancelled"}
	}
	s.status = status
	s.firstCall = false
	return s.status
}
func (s *Sandbox) probe(parent context.Context, l lease.Lease) (Status, error) {
	if err := lease.Do(parent, l, func(context.Context) error { return nil }); err != nil {
		return Status{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	c, err := normalize(s.config)
	if err != nil {
		return Status{Cause: "invalid_policy"}, nil
	}
	launcher, err := findLauncher()
	if err != nil {
		return Status{Cause: err.Error()}, nil
	}
	// A probe artifact is outside the writable workspace. It is removed on all
	// paths, including a killed or failed launcher.
	dir, err := os.MkdirTemp("", "lina-sandbox-probe-")
	if err != nil {
		return Status{Cause: "probe_setup_failed"}, nil
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "canary")
	if err := os.WriteFile(path, []byte("sandbox-probe"), 0600); err != nil {
		return Status{Cause: "probe_setup_failed"}, nil
	}
	c.ReadDenied = append(c.ReadDenied, dir)
	script := `if [ -r "$1" ]; then exit 91; fi; ` + probeScript()
	err = s.execute(ctx, l, launcher, c, Command{Path: "/bin/sh", Args: []string{"-c", script, "probe", path}})
	if parent.Err() != nil {
		return Status{}, parent.Err()
	}
	if errors.Is(err, lease.ErrInvalid) || errors.Is(err, lease.ErrReleased) {
		return Status{}, err
	}
	if err != nil {
		return Status{Cause: "probe_failed"}, nil
	}
	return Status{Available: true}, nil
}

// Run rechecks confinement before the first tool invocation, including after a
// successful startup probe. Failure latches unsupported until an explicit probe.
func (s *Sandbox) Run(ctx context.Context, l lease.Lease, command Command) error {
	s.mu.Lock()
	if !s.firstCall && (s.status.Available || s.status.Cause == "not_probed") {
		status, err := s.probe(ctx, l)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		s.status = status
		s.firstCall = true
	}
	status := s.status
	s.mu.Unlock()
	if !status.Available {
		return fmt.Errorf("%w: %s", ErrUnavailable, status.Cause)
	}
	c, err := normalize(s.config)
	if err != nil {
		return fmt.Errorf("%w: invalid_policy", ErrUnavailable)
	}
	launcher, err := findLauncher()
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	return s.execute(ctx, l, launcher, c, command)
}
func (s *Sandbox) execute(ctx context.Context, l lease.Lease, launcher string, c Config, command Command) error {
	if s.owner == nil {
		return errors.New("process owner required")
	}
	if !filepath.IsAbs(command.Path) {
		return errors.New("sandbox executable must be absolute")
	}
	p, cleanup, err := prepare(launcher, c, command)
	if err != nil {
		return err
	}
	defer cleanup()
	return s.owner.Run(ctx, l, p)
}

// Resolve existing prefixes, including symlinks, even for not-yet-created
// protected paths. This also accounts for /var -> /private/var on macOS.
func canonical(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", errors.New("absolute clean path required")
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	base, err := canonical(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, filepath.Base(path)), nil
}
func beneath(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator)) || root == string(filepath.Separator)
}
func normalize(c Config) (Config, error) {
	var err error
	c.WorkDir, err = canonical(c.WorkDir)
	if err != nil {
		return c, err
	}
	c.WriteRoots = append([]string{c.WorkDir}, c.WriteRoots...)
	c.ReadDenied = append([]string{}, c.ReadDenied...)
	for i, path := range c.ReadDenied {
		c.ReadDenied[i], err = canonical(path)
		if err != nil || c.ReadDenied[i] == "/" {
			return c, errors.New("invalid protected path")
		}
	}
	for i, path := range c.WriteRoots {
		path, err = canonical(path)
		if err != nil || path == "/" {
			return c, errors.New("invalid write root")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return c, errors.New("write root must exist")
		}
		// Never bind a writable ancestor over an inaccessible mount, nor let a
		// granted child reopen a protected tree.
		for _, deny := range c.ReadDenied {
			if beneath(path, deny) || beneath(deny, path) {
				return c, errors.New("write root overlaps protected path")
			}
		}
		c.WriteRoots[i] = path
	}
	return c, nil
}

// Existing Git metadata, including nested repositories and gitdir indirections,
// remains read-only even where its parent is an authorized writable root.
func gitPaths(roots []string) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	add := func(path string) error {
		resolved, err := canonical(path)
		if err != nil {
			return err
		}
		if !seen[resolved] {
			seen[resolved] = true
			paths = append(paths, resolved)
		}
		return nil
	}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Name() != ".git" {
				return nil
			}
			if err := add(path); err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				data, err := readGitFile(path)
				if err != nil {
					return err
				}
				if value, ok := strings.CutPrefix(string(data), "gitdir: "); ok {
					target := strings.TrimSpace(value)
					if !filepath.IsAbs(target) {
						target = filepath.Join(filepath.Dir(path), target)
					}
					if err := add(target); err != nil {
						return err
					}
				}
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}

// A workspace-controlled gitdir file is not an unbounded input to Core. Check
// the opened descriptor as well as the byte count; a concurrent replacement or
// growth must not turn this into a device read or an enormous allocation.
func readGitFile(path string) ([]byte, error) {
	const limit = 4096
	file, err := openMetadata(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid Git metadata file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("oversized Git metadata file")
	}
	return data, nil
}

// Anchor every writable ancestor of protected metadata. A readonly child mount
// alone does not stop rename(parent), followed by replacement at the old path.
func gitAnchors(roots, metadata []string) []string {
	seen := map[string]bool{}
	for _, root := range roots {
		for _, path := range metadata {
			for parent := filepath.Dir(path); beneath(parent, root) && parent != root; parent = filepath.Dir(parent) {
				seen[parent] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}
