// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Web sends a transient settings request once. Replies never carry its key.
func (c *Client) Web(ctx context.Context, p protocol.WebRequest) (string, error) {
	return c.request(ctx, protocol.KindWebRequest, p)
}
