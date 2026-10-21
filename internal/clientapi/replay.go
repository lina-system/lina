// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (c *Client) Export(ctx context.Context, p protocol.ExportRequest) (string, error) {
	return c.request(ctx, protocol.KindExportRequest, p)
}
func (c *Client) Replay(ctx context.Context, p protocol.ReplayRequest) (string, error) {
	return c.request(ctx, protocol.KindReplayRequest, p)
}
