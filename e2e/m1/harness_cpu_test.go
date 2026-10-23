// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestHarnessCommandsShareCPUBudget(t *testing.T) {
	// Changing the parent runtime budget requires a serial test. Two distinct
	// budgets also detect a child silently falling back to the host's default.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{1, 3} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			previous := runtime.GOMAXPROCS(budget)
			defer runtime.GOMAXPROCS(previous)
			h := newHarness(t, binary)
			child := h.command("harness-cpu-budget")
			child.start(t)
			child.wait(t, 0)
			got, err := strconv.Atoi(strings.TrimSpace(child.stdout.String()))
			if err != nil || got != budget {
				t.Fatalf("child CPU budget = %q, want %d: %v", child.stdout.String(), budget, err)
			}
		})
	}
}
