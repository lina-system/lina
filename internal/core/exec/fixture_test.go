// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"os/exec"
	"runtime"
	"testing"

	"github.com/thisisjun786/lina/kit/tools"
)

func requireSandbox(t *testing.T) {
	t.Helper()
	launcher := "bwrap"
	if runtime.GOOS == "darwin" {
		launcher = "sandbox-exec"
	} else if runtime.GOOS != "linux" {
		t.Skip("verified OS sandbox not implemented")
	}
	if _, err := exec.LookPath(launcher); err != nil {
		t.Skip("OS sandbox launcher absent; unavailable-path tests still run")
	}
}

// Unit fixtures bypass authority only where the assertion is about confinement.
// Network approval scenarios use the actual durable authority service.
type passGate struct{}

func (*passGate) Begin(_ context.Context, c tools.Call, _ tools.Spec, _ tools.Input) (tools.Ticket, error) {
	return tools.Ticket{Key: c.Key}, nil
}
func (*passGate) Finish(context.Context, tools.Ticket, tools.Result) error { return nil }
