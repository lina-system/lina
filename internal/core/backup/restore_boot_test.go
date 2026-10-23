// SPDX-License-Identifier: Apache-2.0
package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/boot"
)

func TestCoreRefusesIncompleteRestoreBeforeOpeningServices(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "restore-boot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, "staging/restore.pending"), 0700); err != nil {
		t.Fatal(err)
	}
	opened := errors.New("opened services with pending restore")
	err = boot.Run(t.Context(), boot.Config{StateRoot: root, Socket: filepath.Join(root, "client.sock"), Services: boot.Services{OpenEvents: func(_ context.Context) (boot.Events, error) { return nil, opened }}})
	if !errors.Is(err, backup.ErrRestorePending) {
		t.Fatalf("unsafe startup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system/core/epoch")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed restore minted epoch")
	}
}
