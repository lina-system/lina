// SPDX-License-Identifier: Apache-2.0

package addon_test

import (
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/proc"
)

// Test binaries register the same explicit helper entry point as their hosts.
// Without it, a sandboxed run re-executes this test binary as its supervisor
// and every helper starts the tests again.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}
