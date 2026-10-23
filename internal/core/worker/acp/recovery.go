// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
)

// restoreHoldLimit bounds live updates kept across a session restore.
const restoreHoldLimit = 256

func restoreSession(ctx context.Context, c *wire.Client, dir string, server wire.McpServer, caps *wire.AgentCapabilities, prior *worker.Thread) (wire.SessionID, []wire.Update, error) {
	resume, load := false, false
	if caps != nil {
		resume = caps.SessionCapabilities != nil && caps.SessionCapabilities.Resume != nil
		load = caps.LoadSession != nil && *caps.LoadSession
	}
	if prior == nil || !resume && !load {
		session, err := c.NewSession(ctx, wire.NewSessionRequest{Cwd: dir, McpServers: []wire.McpServer{server}})
		return session.SessionID, nil, err
	}
	id := wire.SessionID(prior.ID)
	result := make(chan error, 1)
	go func() {
		var err error
		if resume {
			servers := []wire.McpServer{server}
			_, err = c.ResumeSession(ctx, wire.ResumeSessionRequest{SessionID: id, Cwd: dir, McpServers: &servers})
		} else {
			_, err = c.LoadSessionWithoutReplay(ctx, wire.LoadSessionRequest{SessionID: id, Cwd: dir, McpServers: []wire.McpServer{server}})
		}
		result <- err
	}()
	updates, permissions, requests := c.Updates(), c.Permissions(), c.Requests()
	// Live updates received after the restore response belong to the next turn.
	// The hold is bounded like the client's own queue: losing one is not allowed.
	var held []wire.Update
	for {
		select {
		case err := <-result:
			return id, held, err
		case u, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			// Replay stays out of progress, output and effect evidence; a live
			// update after the response is kept for the adapter instead of lost.
			if !u.Replay {
				if len(held) >= restoreHoldLimit {
					return "", nil, wire.ErrOverflow
				}
				held = append(held, u)
			}
		case _, ok := <-permissions:
			if !ok {
				permissions = nil
				continue
			}
			return "", nil, errors.New("ACP restore requested permission before a turn")
		case _, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			return "", nil, errors.New("ACP restore requested input before a turn")
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}
}
