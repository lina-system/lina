// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Account and the relay methods send once. Receive owns their correlated replies.
func (c *Client) Account(ctx context.Context, request protocol.AccountRequest) (string, error) {
	return c.request(ctx, protocol.KindAccountRequest, request)
}
func (c *Client) SignInListening(ctx context.Context, request protocol.SignInListening) (string, error) {
	return c.request(ctx, protocol.KindSignInListening, request)
}
func (c *Client) SignInRedirect(ctx context.Context, request protocol.SignInRedirect) (string, error) {
	return c.request(ctx, protocol.KindSignInRedirect, request)
}
