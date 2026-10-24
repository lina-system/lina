// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// A release build compiles the whole dependency graph again: -trimpath gives it
// cache entries the test binary does not populate, and it costs 150 CPU seconds,
// 165 seconds of wall time on one CPU and 43 seconds on four. A four-vCPU runner
// shares its CPUs with the other scenarios in the shard, so a healthy build needs
// more than two minutes. Four minutes leaves the six-minute package watchdog and
// every scenario deadline unchanged.
const releaseBuildBudget = 4 * time.Minute

// Write the real release output for the host target to output. Callers install
// or run it; the release build itself is the packaging step under acceptance.
func buildRelease(t *testing.T, output string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), releaseBuildBudget)
	defer cancel()
	build := exec.CommandContext(ctx, "node", "scripts/build.mjs", "--out", output,
		"--target", runtime.GOOS+"/"+runtime.GOARCH)
	build.Dir = "../.."
	for _, key := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			build.Env = append(build.Env, key+"="+value)
		}
	}
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("package binaries: %v\n%s", err, data)
	}
	return output
}
