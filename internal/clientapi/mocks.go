// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Mock reads a revision or an asset, or admits Build this. A build needs a stable
// key retained by its caller across retries. It grants no deployment authority.
func (c *Client) Mock(ctx context.Context, p protocol.MockRequest, key string) (string, error) {
	var idempotency *string
	if key != "" {
		idempotency = &key
	}
	return c.requestKey(ctx, protocol.KindMockRequest, p, idempotency)
}
