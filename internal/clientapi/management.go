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
