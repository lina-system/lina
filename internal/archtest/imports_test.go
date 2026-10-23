// SPDX-License-Identifier: Apache-2.0
package archtest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportRules(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	standard := make(map[string]bool)
	// Include platform-specific standard packages (for example syscall/js), even
	// when go list std would omit them on the machine running this test.
	// runtime.GOROOT is deprecated and can point at the wrong tree for a copied
	// test binary, so read the toolchain root from the go command.
	goroot, err := standardLibraryRoot()
	if err != nil {
		t.Fatal(err)
	}
	stdlib := filepath.Join(goroot, "src")
	err = filepath.WalkDir(stdlib, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "cmd" || entry.Name() == "vendor" || entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") {
				return filepath.SkipDir
			}
		} else if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			relative, err := filepath.Rel(stdlib, filepath.Dir(name))
			if err != nil {
				return err
			}
			standard[filepath.ToSlash(relative)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	failures, err := violations(root, standard)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range failures {
		t.Error(failure)
	}
}

func TestStandardLibraryRootReportsGoEnvFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := standardLibraryRoot()
	if err == nil {
		t.Fatal("expected go env GOROOT to fail when go is not on PATH")
	}
	if !strings.Contains(err.Error(), "go env GOROOT") {
		t.Fatalf("error %q does not name go env GOROOT", err)
	}
}

func TestStandardLibraryRootReportsGoEnvStderr(t *testing.T) {
	dir := t.TempDir()
	writeGoStub(t, dir, "#!/bin/sh\necho 'GOROOT lookup failed' >&2\nexit 1\n")
	t.Setenv("PATH", dir)
	_, err := standardLibraryRoot()
	if err == nil || !strings.Contains(err.Error(), "GOROOT lookup failed") {
		t.Fatalf("got %v, want the go env stderr", err)
	}
}

func TestStandardLibraryRootRejectsEmptyGoEnv(t *testing.T) {
	dir := t.TempDir()
	writeGoStub(t, dir, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)
	_, err := standardLibraryRoot()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("got %v, want an empty GOROOT error", err)
	}
}

func writeGoStub(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// standardLibraryRoot reads GOROOT from `go env GOROOT`. runtime.GOROOT is
// deprecated since Go 1.24 and can be wrong for a copied test binary.
func standardLibraryRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		detail := err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			detail = strings.TrimSpace(string(exitErr.Stderr))
		}
		return "", fmt.Errorf("go env GOROOT: %s", detail)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("go env GOROOT: empty output")
	}
	return root, nil
}
