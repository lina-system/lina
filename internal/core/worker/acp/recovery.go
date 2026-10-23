// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
)

func restoreSession(ctx context.Context, c *wire.Client, dir string, server wire.McpServer, caps *wire.AgentCapabilities, prior *worker.Thread) (wire.SessionID, error) {
	resume, load := false, false
	if caps != nil {
		resume = caps.SessionCapabilities != nil && caps.SessionCapabilities.Resume != nil
		load = caps.LoadSession != nil && *caps.LoadSession
	}
	if prior == nil || !resume && !load {
		session, err := c.NewSession(ctx, wire.NewSessionRequest{Cwd: dir, McpServers: []wire.McpServer{server}})
		return session.SessionID, err
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
	for {
		select {
		case err := <-result:
			return id, err
		case _, ok := <-updates:
			if !ok {
				updates = nil
			}
			// The load response follows its replay. Drain while waiting, without
			// assigning old activity to the replacement turn or filling the queue.
		case _, ok := <-permissions:
			if !ok {
				permissions = nil
				continue
			}
			return "", errors.New("ACP restore requested permission before a turn")
		case _, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			return "", errors.New("ACP restore requested input before a turn")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
