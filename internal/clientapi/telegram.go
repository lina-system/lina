// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Telegram sends personal-source settings through the owner client route.
func (c *Client) Telegram(ctx context.Context, p protocol.TelegramRequest) (string, error) {
	return c.request(ctx, protocol.KindTelegramRequest, p)
}
