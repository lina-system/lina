// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"fmt"
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

func (c *Client) Progress(ctx context.Context, request protocol.ProgressGet) (string, error) {
	return c.request(ctx, protocol.KindProgressGet, request)
}

func (c *Client) Waiting(ctx context.Context, request protocol.WaitingGet) (string, error) {
	return c.request(ctx, protocol.KindWaitingGet, request)
}

// Control sends an explicit owner request once; Core owns its effect and reply.
func (c *Client) Control(ctx context.Context, p protocol.Payload) (string, error) {
	switch p.(type) {
	case protocol.ProgressDigestGet:
		return c.request(ctx, protocol.KindProgressDigestGet, p)
	case protocol.Stop:
		return c.request(ctx, protocol.KindStop, p)
	case protocol.FollowUp:
		return c.request(ctx, protocol.KindFollowUp, p)
	case protocol.MultiTaskControl:
		return c.request(ctx, protocol.KindMultiTaskControl, p)
	}
	return "", fmt.Errorf("unsupported owner control")
}
