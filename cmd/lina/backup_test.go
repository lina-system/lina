// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCommandHelp(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"backup"}, {"backup", "--help"}} {
		out, err := exec.Command(binary, args...).CombinedOutput()
		want := 64
		if len(args) > 1 {
			want = 0
		}
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want || !strings.Contains(string(out), "--state-root") {
			t.Errorf("%v: exit=%d want=%d, missing backup argument help: %s", args, code, want, out)
		}
	}
}
