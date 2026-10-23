// SPDX-License-Identifier: Apache-2.0
package secrets

import "github.com/thisisjun786/lina/kit/secret"

// Core and clients share one credential-shape detector. Storage still owns
// admission and sensitivity metadata; client masking cannot replace that gate.
type Span = secret.Span
type Masked = secret.Masked

var ErrSpan = secret.ErrSpan

func Mask(text string, flagged ...Span) (Masked, error) {
	return secret.Mask(text, flagged...)
}
