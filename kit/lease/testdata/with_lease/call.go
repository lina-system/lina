// SPDX-License-Identifier: Apache-2.0

package fixture

import (
	"context"
	"github.com/thisisjun786/lina/kit/lease"
)

func call(ctx context.Context, l lease.Lease) error {
	return lease.Do(ctx, l, func(context.Context) error { return nil })
}
