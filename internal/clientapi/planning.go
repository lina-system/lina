// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (c *Client) Plan(ctx context.Context, request protocol.PlanRequest) (string, error) {
	return c.request(ctx, protocol.KindPlanRequest, request)
}
