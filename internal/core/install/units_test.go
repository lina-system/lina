// SPDX-License-Identifier: Apache-2.0
package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSystemdAcceptsRenderedUnits(t *testing.T) {
	analyzer, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd unit parser is unavailable")
	}
	base := filepath.Join(t.TempDir(), `space $dollar %specifier`)
	binary := filepath.Join(base, "lina")
	write(t, binary, []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexit 0\n"), 0700)
	root := filepath.Join(base, "state")
	core, err := coreUnit(binary, root, filepath.Join(root, "run/client.sock"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := workerUnit(binary, root, filepath.Join(root, "run/moirai.sock"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, unit := range map[string]string{"lina-core.service": core, "lina-moirai.service": worker} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(unit), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), analyzer, "verify", filepath.Join(dir, "lina-core.service"), filepath.Join(dir, "lina-moirai.service"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LC_ALL=C", "SYSTEMD_LOG_LEVEL=err"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("systemd refused installed service arguments: %v\n%s", err, out)
	}
}

func TestUnsupportedServiceExecutablePathsAreRefused(t *testing.T) {
	for _, part := range []string{"quote\"path", "single'quote", "backslash\\path", "control\tpath"} {
		root := filepath.Join(t.TempDir(), part)
		if _, err := coreUnit(filepath.Join(root, "lina"), root, filepath.Join(root, "socket")); err == nil {
			t.Fatalf("accepted unsupported executable path %q", part)
		}
	}
}
