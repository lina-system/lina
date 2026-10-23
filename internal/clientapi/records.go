// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Record sends one explicit owner request. Receive owns its correlated reply.
func (c *Client) Record(ctx context.Context, p protocol.RecordRequest) (string, error) {
	return c.request(ctx, protocol.KindRecordRequest, p)
}
