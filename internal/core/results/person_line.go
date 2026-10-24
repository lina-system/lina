// SPDX-License-Identifier: Apache-2.0
package results

import "errors"

// ErrPersonLine rejects a start line or summary the person must not see.
var ErrPersonLine = errors.New("person_line_rejected")

// RejectPersonLine is the check for a goal start line or closing summary.
// The real check lands with the goal lines; this stub fails closed.
func RejectPersonLine(string, []string) error { return ErrPersonLine }
