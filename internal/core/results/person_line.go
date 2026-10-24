// SPDX-License-Identifier: Apache-2.0
package results

import "github.com/thisisjun786/lina/internal/core/work"

// ErrPersonLine rejects a start line or summary that names an id, an issue or
// pull request number, a branch, a path or a check name.
var ErrPersonLine = work.ErrPersonLine

// RejectPersonLine accepts one plain line. forbidden holds check names and
// other tokens that are technical even when they are ordinary words.
func RejectPersonLine(text string, forbidden []string) error {
	return work.RejectPersonLine(text, forbidden)
}
