// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestM3TailnetCoreRefusesPublicBind(t *testing.T) {
	h := newHarness(t, buildLina(t))
	directory := filepath.Join(h.root, "system/core")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := h.command("core", "--state-root", h.root, "--socket", h.socket, "--tailnet-listen", "0.0.0.0:9000")
	p.start(t)
	p.wait(t, 78)
	if _, err := os.Stat(h.socket); !os.IsNotExist(err) {
		t.Fatalf("local socket leaked after startup refusal: %v", err)
	}
}
