// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// MiniApp sends one registry request. Receive owns its correlated reply.
func (c *Client) MiniApp(ctx context.Context, p protocol.MiniAppRequest) (string, error) {
	return c.request(ctx, protocol.KindMiniAppRequest, p)
}
