// SPDX-License-Identifier: Apache-2.0

package proc_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/proc"
)

// Test binaries register the same explicit helper entry point as their hosts.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-job" {
		os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
	}
	// macOS runs this binary again inside a launchd job. If that launch ever
	// reaches the test suite instead of the helper above, running the suite
	// would register more jobs from inside a job — the recursion that exhausted
	// the host. Fail the run instead of starting anything.
	if depth := os.Getenv(proc.JobDepthVariable); depth != "" {
		fmt.Fprintf(os.Stderr, "proc tests must not run inside a process job (depth %s)\n", depth)
		os.Exit(70)
	}
	os.Exit(m.Run())
}
