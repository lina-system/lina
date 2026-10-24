// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Binding reads the chip, the move choices and the recorded moves of one input.
func (c *Client) Binding(ctx context.Context, request protocol.BindingGet) (string, error) {
	return c.request(ctx, protocol.KindBindingGet, request)
}

// BindingStats reads the move count per hundred bindings by decider.
func (c *Client) BindingStats(ctx context.Context) (string, error) {
	return c.request(ctx, protocol.KindBindingStatsGet, protocol.BindingStatsGet{})
}

// BindingExport writes one replay case per recorded move.
func (c *Client) BindingExport(ctx context.Context) (string, error) {
	return c.request(ctx, protocol.KindBindingExport, protocol.BindingExport{})
}

// BindingMove moves one input's binding to the chosen target.
func (c *Client) BindingMove(ctx context.Context, request protocol.BindingMove) (string, error) {
	return c.request(ctx, protocol.KindBindingMove, request)
}
