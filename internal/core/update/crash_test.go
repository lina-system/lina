// SPDX-License-Identifier: Apache-2.0
package update

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestSwapCrashHelper(t *testing.T) {
	if os.Getenv("LINA_UPDATE_CRASH_HELPER") != "1" {
		return
	}
	root, at := os.Getenv("LINA_UPDATE_ROOT"), os.Getenv("LINA_UPDATE_BOUNDARY")
	r := ctlfiles.UpdateRecord{Root: root, Previous: filepath.Join(root, "system/updates/previous-attempt")}
	if err := Swap(r, func(boundary string) error {
		if boundary == at {
			os.Stdout.WriteString("boundary\n")
			select {}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestKilledSwapRestoresWholeComposition(t *testing.T) {
	for _, boundary := range []string{"kept-bin", "swapped-bin", "kept-adapters", "swapped-adapters"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			previous := filepath.Join(root, "system/updates/previous-attempt")
			for _, dir := range []string{"system/bin", "system/adapters", "system/updates/previous-attempt-next/bin", "system/updates/previous-attempt-next/adapters"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"bin", "adapters"} {
				os.WriteFile(filepath.Join(root, "system", name, "artifact"), []byte("previous"), 0600)
				os.WriteFile(filepath.Join(previous+"-next", name, "artifact"), []byte("candidate"), 0600)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSwapCrashHelper$")
			cmd.Env = []string{"LINA_UPDATE_CRASH_HELPER=1", "LINA_UPDATE_ROOT=" + root, "LINA_UPDATE_BOUNDARY=" + boundary}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "boundary" {
				t.Fatal("missing crash acknowledgement")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			r := ctlfiles.UpdateRecord{Root: root, Previous: previous}
			if err = RestoreComposition(r); err != nil {
				t.Fatal(err)
			}
			if err = RestoreComposition(r); err != nil {
				t.Fatal("repeat recovery", err)
			}
			for _, name := range []string{"bin", "adapters"} {
				data, err := os.ReadFile(filepath.Join(root, "system", name, "artifact"))
				if err != nil || string(data) != "previous" {
					t.Fatalf("half composition after %s: %s %v", boundary, data, err)
				}
			}
		})
	}
}
