// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"

	"github.com/thisisjun786/lina/kit/tools"
)

// Unit fixtures bypass authority only where the assertion is about confinement.
// Network approval scenarios use the actual durable authority service.
type passGate struct{}

func (*passGate) Begin(_ context.Context, c tools.Call, _ tools.Spec, _ tools.Input) (tools.Ticket, error) {
	return tools.Ticket{Key: c.Key}, nil
}
func (*passGate) Finish(context.Context, tools.Ticket, tools.Result) error { return nil }
