// SPDX-License-Identifier: Apache-2.0
package buildinfo_test

import (
	"bytes"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "rev-parse", "HEAD")
	git.Dir = root
	output, err := git.Output()
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.TrimSpace(string(output))
	for _, name := range []string{"lina", "lina-node"} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, flags, want string
			}{
				{"development", "", "0.0.1-alpha.dev (commit " + revision + ")\n"},
				{"release", "-X github.com/thisisjun786/lina/internal/buildinfo.Version=0.0.1-alpha.20261020.3 -X github.com/thisisjun786/lina/internal/buildinfo.Commit=0123456789abcdef0123456789abcdef01234567 -X github.com/thisisjun786/lina/internal/buildinfo.Date=2026-10-20", "0.0.1-alpha.20261020.3 (commit 0123456, built 2026-10-20)\n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					binary := build(t, root, name, tc.flags)
					info, err := buildinfo.ReadFile(binary)
					if err != nil {
						t.Fatal(err)
					}
					var embedded string
					for _, setting := range info.Settings {
						if setting.Key == "vcs.revision" {
							embedded = setting.Value
						}
					}
					if embedded != revision {
						t.Fatalf("embedded revision = %q, want %q", embedded, revision)
					}
					checkVersion(t, binary, tc.want)
				})
			}
		})
	}
}

func TestVersionsOutsideRepository(t *testing.T) {
	// Build actual command sources from a source export, without a .git directory.
	root := t.TempDir()
	for _, dir := range []string{"cmd/lina", "cmd/lina-node", "internal/buildinfo", "internal/core", "kit"} {
		if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join("../..", dir))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := os.ReadFile(filepath.Join("../..", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"lina", "lina-node"} {
		t.Run(name, func(t *testing.T) {
			checkVersion(t, build(t, root, name, ""), "0.0.1-alpha.dev (commit unknown)\n")
		})
	}
}

func build(t *testing.T, root, name, flags string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	args := []string{"build", "-mod=readonly", "-o", binary}
	if flags != "" {
		args = append(args, "-ldflags", flags)
	}
	args = append(args, "./cmd/"+strings.TrimSuffix(name, ".exe"))
	command := exec.Command("go", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}
	return binary
}

func checkVersion(t *testing.T, binary, want string) {
	t.Helper()
	command := exec.Command(binary, "version")
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "PATH=")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("version: %v; stdout=%q stderr=%q", err, &stdout, &stderr)
	}
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q; want stdout=%q and no stderr", &stdout, &stderr, want)
	}
}
