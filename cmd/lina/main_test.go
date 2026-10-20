// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	build := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build command: %v\n%s", err, output)
	}
	root := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-command-test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	uninstalled := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(uninstalled, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{name: "missing", code: 64},
		{name: "unknown", args: []string{"unknown"}, code: 64},
		{name: "core missing socket", args: []string{"core"}, code: 64},
		{name: "core empty socket", args: []string{"core", "--socket="}, code: 64},
		{name: "core unknown flag", args: []string{"core", "--unknown"}, code: 64},
		{name: "core extra argument", args: []string{"core", "--socket", "unused.sock", "extra"}, code: 64},
		{name: "core missing state root", args: []string{"core", "--socket", "unused.sock"}, code: 64},
		{name: "core missing socket with root", args: []string{"core", "--state-root", root}, code: 64},
		{name: "core missing installation", args: []string{"core", "--state-root", filepath.Join(root, "missing"), "--socket", "unused.sock"}, code: 78},
		{name: "core missing main id", args: []string{"core", "--state-root", uninstalled, "--socket", "unused.sock"}, code: 78},
		{name: "core non-local root", args: []string{"core", "--state-root", "/proc", "--socket", "unused.sock"}, code: 78},
		{name: "core occupied socket path", args: []string{"core", "--state-root", root, "--socket", binary}, code: 78},
		{name: "tui missing socket", args: []string{"tui"}, code: 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command(binary, tc.args...)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
				t.Fatalf("exit = %v, want %d; stderr: %s", err, tc.code, &stderr)
			}
			if tc.name == "core non-local root" && !strings.Contains(stderr.String(), "supported local filesystem") {
				t.Fatalf("filesystem check was not reached: %s", &stderr)
			}
			if stdout.Len() != 0 || strings.TrimSpace(stderr.String()) == "" {
				t.Fatalf("expected only stderr diagnostic, got stdout=%q stderr=%q", &stdout, &stderr)
			}
			if tc.code == 64 && !strings.Contains(strings.ToLower(stderr.String()), "usage:") {
				t.Fatalf("missing usage diagnostic: %s", &stderr)
			}
		})
	}
}
