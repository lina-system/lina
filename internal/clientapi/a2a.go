// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// A2A sends owner settings transiently. Credentials never enter conversation.
func (c *Client) A2A(ctx context.Context, p protocol.A2aRequest) (string, error) {
	return c.request(ctx, protocol.KindA2aRequest, p)
}
