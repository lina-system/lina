// SPDX-License-Identifier: Apache-2.0

// Package ghclient observes GitHub pull requests through Core-owned gh children.
package ghclient

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

type Ref struct {
	Repository string
	Number     int
}
type Check struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
}
type Comment struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	Path string `json:"path"`
	URL  string `json:"url"`
}
type Review struct {
	ID         string    `json:"id"`
	IsResolved bool      `json:"isResolved"`
	IsOutdated bool      `json:"isOutdated"`
	Comments   []Comment `json:"comments"`
}
type Snapshot struct {
	Ref                    Ref
	Head, State, CI, Cause string
	Checks                 []Check
	Reviews                []Review
	RetryAt                time.Time
	Done                   bool
	Revision               uint64
}
type Config struct {
	Path   string
	Env    []string
	Leaser lease.Leaser
}
type runner interface {
	Run(context.Context, lease.Lease, proc.Command) error
}
type Client struct {
	ctx     context.Context
	cancel  context.CancelFunc
	cfg     Config
	owner   *proc.Owner
	runner  runner
	mu      sync.Mutex
	watches map[Ref]*Watch
	closed  bool
	wg      sync.WaitGroup
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func normalize(ref Ref) (Ref, error) {
	if !repoPattern.MatchString(ref.Repository) || ref.Number <= 0 || strings.HasSuffix(ref.Repository, "/.") || strings.HasSuffix(ref.Repository, "/..") {
		return Ref{}, errors.New("invalid GitHub pull request")
	}
	ref.Repository = strings.ToLower(ref.Repository)
	return ref, nil
}

// New does not contact GitHub. Missing gh affects observations, never Core boot.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Leaser == nil {
		return nil, errors.New("GitHub observations require an activity registry")
	}
	if cfg.Env == nil {
		cfg.Env = proc.InheritedEnvironment("LINA_RESPONSES_API_KEY")
	} else {
		cfg.Env = proc.Environment(cfg.Env, "LINA_RESPONSES_API_KEY")
	}
	cfg.Env = append(cfg.Env, "GH_PROMPT_DISABLED=1", "GH_PAGER=cat", "GH_DEBUG=", "GH_FORCE_TTY=0")
	if cfg.Path == "" {
		for _, entry := range cfg.Env {
			if path, ok := strings.CutPrefix(entry, "PATH="); ok {
				for _, dir := range filepath.SplitList(path) {
					if !filepath.IsAbs(dir) {
						continue
					}
					candidate := filepath.Join(dir, "gh")
					if info, e := os.Stat(candidate); e == nil && !info.IsDir() && info.Mode()&0111 != 0 {
						cfg.Path = candidate
						break
					}
				}
			}
		}
	}
	if cfg.Path != "" && !filepath.IsAbs(cfg.Path) {
		return nil, errors.New("gh path must be absolute")
	}
	ctx, cancel := context.WithCancel(ctx)
	owner := proc.NewOwner()
	return &Client{ctx: ctx, cancel: cancel, cfg: cfg, owner: owner, runner: owner, watches: make(map[Ref]*Watch)}, nil
}

// Read is one explicit observation need. It never starts a watcher or retries.
func (c *Client) Read(ctx context.Context, ref Ref) (Snapshot, error) {
	ref, err := normalize(ref)
	if err != nil {
		return Snapshot{}, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Snapshot{}, proc.ErrClosed
	}
	if w := c.watches[ref]; w != nil {
		w.mu.Lock()
		ended := w.ended
		w.mu.Unlock()
		if ended {
			delete(c.watches, ref)
		}
	}
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	s, _, err := c.observe(ctx, ref, &observation{})
	return s, err
}

// Watch shares a live watcher between all callers, including differently cased
// repository names. Terminal snapshots remain available until an explicit Read
// starts a new need, or until eviction from the bounded observation cache.
func (c *Client) Watch(ref Ref) (*Watch, error) {
	ref, err := normalize(ref)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, proc.ErrClosed
	}
	if w := c.watches[ref]; w != nil {
		return w, nil
	}
	if len(c.watches) >= 256 {
		for key, old := range c.watches {
			old.mu.Lock()
			ended := old.ended
			old.mu.Unlock()
			if ended {
				delete(c.watches, key)
				break
			}
		}
		if len(c.watches) >= 256 {
			return nil, errors.New("GitHub watcher capacity reached")
		}
	}
	w := &Watch{changed: make(chan struct{})}
	c.watches[ref] = w
	c.wg.Add(1)
	go c.watch(ref, w)
	return w, nil
}

type Watch struct {
	mu       sync.Mutex
	snapshot Snapshot
	changed  chan struct{}
	ended    bool
}

func clone(s Snapshot) Snapshot {
	s.Checks = append([]Check{}, s.Checks...)
	s.Reviews = append([]Review{}, s.Reviews...)
	for i := range s.Reviews {
		s.Reviews[i].Comments = append([]Comment{}, s.Reviews[i].Comments...)
	}
	return s
}

// Next waits for a newer observation. Cancelling one reader leaves the shared
// watcher alive. EOF means this handle has no further observations.
func (w *Watch) Next(ctx context.Context, after uint64) (Snapshot, error) {
	for {
		w.mu.Lock()
		s := clone(w.snapshot)
		ended := w.ended
		ch := w.changed
		w.mu.Unlock()
		if s.Revision > after {
			return s, nil
		}
		if ended {
			return s, io.EOF
		}
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-ch:
		}
	}
}
func (w *Watch) publish(s Snapshot, end bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s.Revision = w.snapshot.Revision + 1
	w.snapshot = clone(s)
	w.ended = end
	close(w.changed)
	w.changed = make(chan struct{})
}
func (c *Client) watch(ref Ref, w *Watch) {
	defer c.wg.Done()

	cache := &observation{}
	for {
		s, delay, err := c.observe(c.ctx, ref, cache)
		end := s.Done || (err != nil && s.Cause != "rate_limited" && s.Cause != "head_changed")
		if c.ctx.Err() != nil {
			s.CI = "pending"
			s.Cause = "cancelled"
			end = true
		}
		w.publish(s, end)
		if end {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			s.CI = "pending"
			s.Cause = "cancelled"
			w.publish(s, true)
			return
		case <-timer.C:
		}
	}
}
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	err := c.owner.Close(ctx)
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}
