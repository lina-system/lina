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
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{name: "missing", code: 64},
		{name: "unknown", args: []string{"unknown"}, code: 64},
		{name: "core", args: []string{"core"}, code: 70},
		{name: "tui", args: []string{"tui"}, code: 70},
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
			if stdout.Len() != 0 || strings.TrimSpace(stderr.String()) == "" {
				t.Fatalf("expected only stderr diagnostic, got stdout=%q stderr=%q", &stdout, &stderr)
			}
			if tc.code == 64 && !strings.Contains(strings.ToLower(stderr.String()), "usage:") {
				t.Fatalf("missing usage diagnostic: %s", &stderr)
			}
		})
	}
}
