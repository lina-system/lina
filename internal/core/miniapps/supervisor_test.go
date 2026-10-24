// SPDX-License-Identifier: Apache-2.0

package miniapps

import (
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/proc"
)

// Test binaries register the same explicit helper entry point as their hosts:
// without it a re-executed test binary runs the tests again and spawns another
// supervisor, forever (hx OOM 10-23, 134 miniapps.test processes).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-job" {
		os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}
