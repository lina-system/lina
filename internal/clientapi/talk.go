// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Talk and ExpandTalk use the same correlated receive path as conversation.
func (c *Client) Talk(ctx context.Context, p protocol.AgentTalkGet) (string, error) {
	return c.request(ctx, protocol.KindAgentTalkGet, p)
}
func (c *Client) ExpandTalk(ctx context.Context, p protocol.AgentTalkExpand) (string, error) {
	return c.request(ctx, protocol.KindAgentTalkExpand, p)
}
