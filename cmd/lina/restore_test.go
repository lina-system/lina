// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreCommandHelp(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	if output, err := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"restore"}, 64},
		{[]string{"restore", "--help"}, 0},
		{[]string{"restore", "--state-root", t.TempDir()}, 64},
		{[]string{"restore", "--generation", "missing"}, 64},
		{[]string{"restore", "--unknown"}, 64},
	} {
		command := exec.Command(binary, tc.args...)
		output, _ := command.CombinedOutput()
		if command.ProcessState.ExitCode() != tc.code {
			t.Errorf("%v: exit %d, want %d: %s", tc.args, command.ProcessState.ExitCode(), tc.code, output)
		}
		if !strings.Contains(string(output), "lina restore") || !strings.Contains(string(output), "--generation") || !strings.Contains(string(output), "--state-root") {
			t.Errorf("%v: missing restore-specific argument help: %s", tc.args, output)
		}
	}
}
