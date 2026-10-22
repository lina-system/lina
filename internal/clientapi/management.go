// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Agent and Memory send once; Receive owns their correlated replies.
func (c *Client) Agent(ctx context.Context, request protocol.AgentRequest) (string, error) {
	return c.request(ctx, protocol.KindAgentRequest, request)
}
func (c *Client) Memory(ctx context.Context, request protocol.MemoryRequest) (string, error) {
	return c.request(ctx, protocol.KindMemoryRequest, request)
}

func (c *Client) Running(ctx context.Context, request protocol.RunningGet) (string, error) {
	return c.request(ctx, protocol.KindRunningGet, request)
}

// Work sends an owner operation once; Receive owns the correlated result.
func (c *Client) Work(ctx context.Context, request protocol.WorkSwitchRequest) (string, error) {
	return c.request(ctx, protocol.KindWorkSwitchRequest, request)
}

func (c *Client) Progress(ctx context.Context, request protocol.ProgressGet) (string, error) {
	return c.request(ctx, protocol.KindProgressGet, request)
}

func (c *Client) Waiting(ctx context.Context, request protocol.WaitingGet) (string, error) {
	return c.request(ctx, protocol.KindWaitingGet, request)
}
