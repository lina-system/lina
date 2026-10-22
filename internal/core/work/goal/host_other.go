// SPDX-License-Identifier: Apache-2.0
//go:build !linux

package goal

import "context"

// An unavailable platform measurement gives no free capacity. Installed Node
// integrations can supply the native report through NodeChanged instead.
func MainHost(context.Context, string, string, Resources, map[string]bool) (Node, error) {
	return Node{}, ErrInvalid
}
