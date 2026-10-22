// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (c *Client) Restart(ctx context.Context, mode protocol.RestartMode) (string, error) {
	return c.request(ctx, protocol.KindRestartRequest, protocol.RestartRequest{Mode: &mode})
}
