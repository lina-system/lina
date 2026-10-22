// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImportHermesCommandRegistered(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	if out, err := exec.Command(binary, "import-hermes", "--help").CombinedOutput(); err != nil {
		t.Fatalf("installed command missing: %v %s", err, out)
	}
}
