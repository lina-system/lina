// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Teams sends owner connection settings through the authenticated client route.
func (c *Client) Teams(ctx context.Context, p protocol.TeamsRequest) (string, error) {
	return c.request(ctx, protocol.KindTeamsRequest, p)
}
