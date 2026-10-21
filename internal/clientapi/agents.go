// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Agent sends one agent operation; Receive owns the correlated result.
func (c *Client) Agent(ctx context.Context, request protocol.AgentRequest) (string, error) {
	return c.request(ctx, protocol.KindAgentRequest, request)
}
