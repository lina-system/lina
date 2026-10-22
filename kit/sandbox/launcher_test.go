// SPDX-License-Identifier: Apache-2.0
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLauncherFallbackLocations(t *testing.T) {
	// Exercise every supported installation prefix independently of the test host.
	for _, location := range []string{"/usr/bin/bwrap", "/bin/bwrap", "/usr/local/bin/bwrap", "/opt/homebrew/bin/bwrap", "/home/linuxbrew/.linuxbrew/bin/bwrap", "/usr/bin/sandbox-exec"} {
		t.Run(location, func(t *testing.T) {
			goos := "linux"
			if location == "/usr/bin/sandbox-exec" {
				goos = "darwin"
			}
			got, err := resolveHostLauncher(goos, "arm64", func(candidate string) (string, error) {
				if candidate == location {
					return candidate, nil
				}
				return "", exec.ErrNotFound
			})
			if err != nil || got != location {
				t.Fatalf("launcher=%q %v", got, err)
			}
		})
	}
}
func TestLauncherRejectsNonExecutableAndRelative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher")
	if err := os.WriteFile(path, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLauncher("", []string{path}, exec.LookPath); err == nil {
		t.Fatal("accepted non-executable")
	}
	if _, err := resolveLauncher("bwrap", nil, func(string) (string, error) { return "./bwrap", nil }); err == nil {
		t.Fatal("accepted relative executable")
	}
}
func TestInstalledLauncherWithoutPATH(t *testing.T) {
	t.Setenv("PATH", "")
	want := "/usr/bin/bwrap"
	if runtime.GOOS == "darwin" {
		want = "/usr/bin/sandbox-exec"
	}
	if _, err := os.Stat(want); err != nil {
		t.Skip("host has no standard launcher")
	}
	got, err := findLauncher()
	if err != nil || got != want {
		t.Fatalf("launcher=%q %v", got, err)
	}
}
