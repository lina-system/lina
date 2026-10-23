// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Result reads one result; Receive owns the correlated reply.
func (c *Client) Result(ctx context.Context, p protocol.ResultGet) (string, error) {
	return c.request(ctx, protocol.KindResultGet, p)
}
