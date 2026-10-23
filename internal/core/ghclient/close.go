// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

type CloseResult struct {
	State, Cause, Head string
}

// ClosePR records intent before a fixed-argument close with its reason. Once
// intent commits, errors are uncertain: callers may only ObserveClosed.
func (c *Client) ClosePR(ctx context.Context, ref Ref, base, head, reason string, admit func(context.Context) error) (CloseResult, error) {
	ref, err := normalize(ref)
	if err != nil || !validBranch(base) || !shaPattern.MatchString(head) || strings.TrimSpace(reason) == "" || len(reason) > 1<<20 || strings.ContainsRune(reason, '\x00') || admit == nil {
		return CloseResult{}, errors.New("invalid_close_request")
	}
	ctx, done, err := c.operation(ctx)
	if err != nil {
		return CloseResult{}, err
	}
	defer done()
	release, err := c.takeSlot(ctx, ref.Repository+"/"+base)
	if err != nil {
		return CloseResult{}, err
	}
	defer release()
	r, err := c.readClosed(ctx, ref, base, head)
	if err != nil || r.State != "open" {
		return r, err
	}
	if err = admit(ctx); err != nil {
		return r, err
	}
	r.State, r.Cause = "unknown", "close_outcome_unknown"
	l, err := c.cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "GitHub close with delivery reason"})
	if err != nil {
		return r, err
	}
	defer l.Release()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runErr := c.runner.Run(deadline, l, proc.Command{Path: c.cfg.Path, Args: []string{"pr", "close", strconv.Itoa(ref.Number), "--repo", "github.com/" + ref.Repository, "--comment", reason}, Env: c.cfg.Env, Stdout: io.Discard, Stderr: io.Discard})
	observed, err := c.readClosed(ctx, ref, base, head)
	if err != nil {
		return r, errors.Join(runErr, err)
	}
	if observed.State == "closed" {
		return observed, runErr
	}
	return r, runErr
}

func (c *Client) ObserveClosed(ctx context.Context, ref Ref, base, head string) (CloseResult, error) {
	ref, err := normalize(ref)
	if err != nil || !validBranch(base) || !shaPattern.MatchString(head) {
		return CloseResult{}, errors.New("invalid_close_request")
	}
	ctx, done, err := c.operation(ctx)
	if err != nil {
		return CloseResult{}, err
	}
	defer done()
	return c.readClosed(ctx, ref, base, head)
}

func (c *Client) readClosed(ctx context.Context, ref Ref, base, head string) (CloseResult, error) {
	p, err := c.pull(ctx, ref)
	r := CloseResult{State: "unknown", Head: p.Head.SHA}
	if err != nil {
		return r, err
	}
	if p.Head.SHA != head {
		r.Cause = "head_changed"
	} else if p.Base.Ref != base {
		r.Cause = "base_changed"
	} else if p.Merged {
		r.Cause = "already_merged"
	} else {
		r.State = p.State
	}
	return r, nil
}
