// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// KakaoTalk sends one control request for the KakaoTalk source. Every
// operation is answered by one `kakaotalk_result`, including each step of the
// add-on's sign-in flow.
func (c *Client) KakaoTalk(ctx context.Context, request protocol.KakaotalkRequest) (string, error) {
	return c.request(ctx, protocol.KindKakaotalkRequest, request)
}
