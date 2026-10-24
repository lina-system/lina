// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Webex sends one control request for a messenger source. The reply is
// received through the connection: `webex_result` for every operation,
// and the browser relay's frames for sign-in.
func (c *Client) Webex(ctx context.Context, request protocol.WebexRequest) (string, error) {
	return c.request(ctx, protocol.KindWebexRequest, request)
}
