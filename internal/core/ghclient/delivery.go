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

// ReadBranch reads PR delivery metadata for a Core-selected task branch. The
// caller verifies the returned branch/base/head; worker prose is never a selector.
func (c *Client) ReadBranch(ctx context.Context, repository, branch string) ([]byte, error) {
	if !repoPattern.MatchString(repository) || branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, "\x00\r\n") {
		return nil, errors.New("invalid PR branch lookup")
	}
	return c.deliveryJSON(ctx, []string{"pr", "view", branch, "--repo", "github.com/" + repository, "--json", "number,url,state,isDraft,headRefName,baseRefName,headRefOid,statusCheckRollup,reviews"})
}
func (c *Client) ReadComments(ctx context.Context, repository string, number int) ([]byte, error) {
	ref, err := normalize(Ref{Repository: repository, Number: number})
	if err != nil {
		return nil, err
	}
	return c.deliveryJSON(ctx, []string{"api", "--hostname", "github.com", "--paginate", "--slurp", "repos/" + ref.Repository + "/pulls/" + strconv.Itoa(number) + "/comments"})
}
func (c *Client) deliveryJSON(ctx context.Context, args []string) ([]byte, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, proc.ErrClosed
	}
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()
	if c.cfg.Path == "" {
		return nil, &readError{cause: "gh_unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	l, err := c.cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "worker PR delivery observation"})
	if err != nil {
		return nil, err
	}
	defer l.Release()
	var out bounded
	err = c.runner.Run(ctx, l, proc.Command{Path: c.cfg.Path, Args: args, Env: c.cfg.Env, Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return nil, &readError{cause: "read_failed"}
	}
	if out.overflow {
		return nil, &readError{cause: "response_too_large"}
	}
	return out.Bytes(), nil
}
