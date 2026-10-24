// SPDX-License-Identifier: Apache-2.0
package jobapi

import "context"

// EmbeddingAPI is an optional bounded memory job. Core selects all sources and
// owns credentials, consent and publication. No text or vector crosses this API.
type EmbeddingAPI interface {
	Embed(context.Context) (EmbeddingResult, error)
}
type EmbeddingResult struct{ Completed int }

func (c *Client) Embed(ctx context.Context) (EmbeddingResult, error) {
	var result EmbeddingResult
	err := c.call(ctx, "Embed", struct{}{}, &result)
	return result, err
}
