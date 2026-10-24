// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func snapshotFiles(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	files := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestNewerStoreExits78Unchanged(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	s.Close(context.Background())
	cfg.Controls.(*ctlfiles.Store).Close()
	root := filepath.Dir(cfg.Path)
	before := snapshotFiles(t, root)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestOlderBinaryProcess$")
	cmd.Env = append(os.Environ(), "LINA_TEST_OLDER_STORE="+cfg.Path)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 78 {
		t.Fatalf("exit=%v output=%s", err, output)
	}
	if after := snapshotFiles(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused open changed store: before=%v after=%v", before, after)
	}
}

func TestOlderBinaryProcess(t *testing.T) {
	t.Parallel()
	path := os.Getenv("LINA_TEST_OLDER_STORE")
	if path == "" {
		return
	}
	controls, err := ctlfiles.Open(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	// This binary supports no numbered data migrations; the store is version 1.
	s, err := Open(t.Context(), Config{Path: path, Epoch: 8, Controls: controls})
	if s != nil {
		s.Close(context.Background())
	}
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("wrong refusal: %v", err)
	}
	var status interface{ ExitCode() int }
	if !errors.As(err, &status) {
		t.Fatalf("missing exit status: %v", err)
	}
	os.Exit(status.ExitCode())
}
