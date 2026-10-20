// SPDX-License-Identifier: Apache-2.0

package fixture

import (
	"context"
	"github.com/thisisjun786/lina/kit/lease"
)

func call(ctx context.Context) error {
	return lease.Do(ctx, func(context.Context) error { return nil })
}
