// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

// MergeResult is evidence, never a delivery verdict. Unknown and queued are not
// landed. Source distinguishes our confirmed call from another actor's result.
type MergeResult struct {
	State, Cause, Head, Base, Commit, Source, CI string
	Checks                                       []Check
}
type pull struct {
	Number int
	State  string
	Merged bool
	Head   struct{ SHA string }
	Base   struct {
		Ref  string
		Repo struct {
			FullName string `json:"full_name"`
		}
	}
	MergeCommit string `json:"merge_commit_sha"`
}

func (c *Client) pull(ctx context.Context, ref Ref) (pull, error) {
	body, _, err := c.request(ctx, endpoint(ref), "", &observation{})
	var p pull
	if err == nil {
		err = decode(body, &p)
	}
	if err == nil && (p.Number != ref.Number || !shaPattern.MatchString(p.Head.SHA) || !validBranch(p.Base.Ref) || !strings.EqualFold(p.Base.Repo.FullName, ref.Repository) || (p.State != "open" && p.State != "closed")) {
		err = errors.New("invalid_merge_evidence")
	}
	return p, err
}
func validBranch(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " ~^:?*[\\\x00\r\n") || strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// operation joins Core shutdown, including slot waiters and reconciliation reads.
func (c *Client) operation(ctx context.Context) (context.Context, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, proc.ErrClosed
	}
	c.wg.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() { stop(); cancel(); c.wg.Done() }, nil
}
func (c *Client) takeSlot(ctx context.Context, key string) (func(), error) {
	c.mu.Lock()
	if c.slots == nil {
		c.slots = map[string]*mergeSlot{}
	}
	s := c.slots[key]
	if s == nil {
		s = &mergeSlot{token: make(chan struct{}, 1)}
		c.slots[key] = s
	}
	s.users++
	c.mu.Unlock()
	releaseUser := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		s.users--
		if s.users == 0 {
			delete(c.slots, key)
		}
	}
	select {
	case s.token <- struct{}{}:
		return func() { <-s.token; releaseUser() }, nil
	case <-ctx.Done():
		releaseUser()
		return nil, ctx.Err()
	}
}

type mergeSlot struct {
	token chan struct{}
	users int
}

var blockingPriority = regexp.MustCompile(`(?i)\bP[01]\b`)

// Merge takes the repository/base slot, checks fresh evidence and calls admit
// immediately before dispatch. admit compares protected refs and durably records
// intent. The caller must never repeat an unknown/queued intent after restart.
func (c *Client) Merge(ctx context.Context, ref Ref, base, head string, admit func(context.Context) error) (MergeResult, error) {
	ref, err := normalize(ref)
	if err != nil || !validBranch(base) || !shaPattern.MatchString(head) || admit == nil {
		return MergeResult{}, errors.New("invalid_merge_request")
	}
	ctx, done, err := c.operation(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	defer done()
	release, err := c.takeSlot(ctx, ref.Repository+"/"+base)
	if err != nil {
		return MergeResult{}, err
	}
	defer release()
	s, err := c.Read(ctx, ref)
	r := MergeResult{State: "blocked", Head: s.Head, Base: base, CI: s.CI, Checks: s.Checks, Cause: s.Cause}
	if err != nil {
		return r, err
	}
	// A merged PR is an observation, including when another actor won the race.
	if s.State == "MERGED" {
		return c.observeMerge(ctx, ref, base)
	}
	if s.Head != head {
		r.Cause = "head_changed"
		return r, nil
	}
	if s.State != "OPEN" {
		r.Cause = "pr_closed"
		return r, nil
	}
	if s.CI != "success" {
		r.Cause = "checks_not_green"
		return r, nil
	}
	for _, review := range s.Reviews {
		if !review.IsResolved {
			for _, comment := range review.Comments {
				if blockingPriority.MatchString(comment.Body) {
					r.Cause = "blocking_review"
					return r, nil
				}
			}
		}
	}
	// Checks/reviews can take multiple reads. Recheck identity at the write boundary.
	p, err := c.pull(ctx, ref)
	if err != nil {
		return r, err
	}
	if p.Base.Ref != base {
		r.Cause = "base_changed"
		return r, nil
	}
	if p.Merged {
		return c.verifyMerge(ctx, ref, base, p)
	}
	if p.Head.SHA != head {
		fresh, e := c.Read(ctx, ref)
		r.Head = fresh.Head
		r.Checks = fresh.Checks
		r.CI = fresh.CI
		r.Cause = "head_changed"
		return r, e
	}
	if p.State != "open" {
		r.Cause = "pr_closed"
		return r, nil
	}
	if err = admit(ctx); err != nil {
		return r, err
	}
	r.State = "unknown"
	r.Cause = "merge_outcome_unknown"
	l, err := c.cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "GitHub head-pinned merge"})
	if err != nil {
		return r, err
	}
	defer l.Release()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Never --admin, --auto, a shell, or a caller-provided strategy.
	runErr := c.runner.Run(deadline, l, proc.Command{Path: c.cfg.Path, Args: []string{"pr", "merge", strconv.Itoa(ref.Number), "--repo", ref.Repository, "--merge", "--match-head-commit", head}, Env: c.cfg.Env, Stdout: io.Discard, Stderr: io.Discard})
	// One confirming read also reconciles a CLI failure; no write retry.
	p, err = c.pull(ctx, ref)
	if err != nil {
		return r, err
	}
	if p.Base.Ref != base {
		return r, errors.New("base_changed")
	}
	if p.Head.SHA != head {
		fresh, e := c.Read(ctx, ref)
		r.Head = fresh.Head
		r.CI = fresh.CI
		r.Checks = fresh.Checks
		r.Cause = "head_changed"
		return r, e
	}
	if p.Merged {
		verified, e := c.verifyMerge(ctx, ref, base, p)
		if e != nil {
			return r, e
		}
		if runErr == nil {
			verified.Source = "receipt"
		}
		return verified, nil
	}
	if runErr == nil {
		r.State = "queued"
		r.Cause = "awaiting_merge_confirmation"
	}
	return r, nil
}
func (c *Client) ObserveMerge(ctx context.Context, ref Ref, base string) (MergeResult, error) {
	ref, err := normalize(ref)
	if err != nil || !validBranch(base) {
		return MergeResult{}, errors.New("invalid_merge_request")
	}
	ctx, done, err := c.operation(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	defer done()
	return c.observeMerge(ctx, ref, base)
}
func (c *Client) observeMerge(ctx context.Context, ref Ref, base string) (MergeResult, error) {
	p, err := c.pull(ctx, ref)
	if err != nil {
		return MergeResult{}, err
	}
	return c.verifyMerge(ctx, ref, base, p)
}
func (c *Client) verifyMerge(ctx context.Context, ref Ref, base string, p pull) (MergeResult, error) {
	r := MergeResult{State: "unconfirmed", Head: p.Head.SHA, Base: base, CI: "pending"}
	if p.Base.Ref != base {
		return r, errors.New("base_changed")
	}
	if !p.Merged {
		r.Cause = "not_merged"
		return r, nil
	}
	if !shaPattern.MatchString(p.MergeCommit) {
		return r, errors.New("merge_commit_missing")
	}
	cache := &observation{}
	body, _, err := c.request(ctx, fmt.Sprintf("repos/%s/commits/%s", ref.Repository, p.MergeCommit), "", cache)
	if err != nil {
		return r, err
	}
	var commit struct{ SHA string }
	if err = decode(body, &commit); err != nil {
		return r, err
	}
	if commit.SHA != p.MergeCommit {
		return r, errors.New("merge_commit_mismatch")
	}
	body, _, err = c.request(ctx, fmt.Sprintf("repos/%s/git/ref/heads/%s", ref.Repository, url.PathEscape(base)), "", cache)
	if err != nil {
		return r, err
	}
	var target struct {
		Ref    string
		Object struct{ Type, SHA string }
	}
	if err = decode(body, &target); err != nil {
		return r, err
	}
	if target.Ref != "refs/heads/"+base || target.Object.Type != "commit" || target.Object.SHA != p.MergeCommit {
		return r, errors.New("target_ref_mismatch")
	}
	r.Checks, _, err = c.checks(ctx, ref, p.MergeCommit, cache)
	if err != nil {
		return r, err
	}
	r.CI = checkConclusion(r.Checks)
	r.State = "merged"
	r.Commit = p.MergeCommit
	r.Source = "observed"
	if r.CI != "success" {
		r.Cause = "merged_without_green_checks"
	}
	return r, nil
}
func checkConclusion(checks []Check) string {
	if len(checks) == 0 {
		return "pending"
	}
	failed := false
	for _, check := range checks {
		if check.State != "completed" {
			return "pending"
		}
		switch check.Conclusion {
		case "success", "neutral", "skipped":
		case "failure", "cancelled", "timed_out", "action_required", "stale", "startup_failure", "error":
			failed = true
		default:
			return "pending"
		}
	}
	if failed {
		return "failure"
	}
	return "success"
}
