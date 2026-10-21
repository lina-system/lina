// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Channel sends a control request once; Receive owns its correlated reply.
func (c *Client) Channel(ctx context.Context, request protocol.ChannelRequest) (string, error) {
	return c.request(ctx, protocol.KindChannelRequest, request)
}
