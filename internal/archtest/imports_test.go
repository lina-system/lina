// SPDX-License-Identifier: Apache-2.0
package archtest

import (
	"io/fs"
	"path/filepath"
	"runtime"
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
	stdlib := filepath.Join(runtime.GOROOT(), "src")
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
