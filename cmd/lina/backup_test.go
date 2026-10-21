// SPDX-License-Identifier: Apache-2.0
package main

import (
	"crypto/sha256"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBackupCommandHelp(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"backup"}, {"backup", "--help"}, {"backup", "--unknown"}, {"backup", "--state-root=", "extra"}} {
		out, err := exec.Command(binary, args...).CombinedOutput()
		want := 64
		if len(args) > 1 && args[1] == "--help" {
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
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	before := backupTreeSnapshot(t, root)
	output, err := exec.Command(binary, "backup", "--state-root", root).CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 69 || !strings.Contains(string(output), "systemctl --user stop lina-core.service") {
		t.Fatalf("running Core refusal: %v %s", err, output)
	}
	if after := backupTreeSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("locked backup wrote state: before=%v after=%v", before, after)
	}

}

func backupTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[relative] = fmt.Sprintf("%s %d %x", info.Mode(), info.ModTime().UnixNano(), sha256.Sum256(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
