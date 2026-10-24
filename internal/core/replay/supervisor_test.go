// SPDX-License-Identifier: Apache-2.0

package replay_test

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/replay"
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
	if len(os.Args) > 1 && os.Args[1] == "replay-worker" {
		if err := replay.Worker(context.Background(), os.Stdin, os.Stdout); err != nil {
			os.Exit(70)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
