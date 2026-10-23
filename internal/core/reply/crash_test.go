// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

type crashCanon struct {
	*canon.Store
	phase string
}

func (s crashCanon) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	err := s.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if s.phase == "before" {
			os.Exit(83)
		}
		return nil
	})
	if err == nil && s.phase == "after" {
		os.Exit(83)
	}
	return err
}
func crashDB(t *testing.T, root string, epoch uint64) *canon.Store {
	t.Helper()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	return openDB(t, canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: canon.Migrations()})
}
func TestRecordingCrashChild(t *testing.T) {
	root := os.Getenv("LINA_REPLY_CRASH_ROOT")
	if root == "" {
		return
	}
	db := crashDB(t, root, 1)
	f := filter(t, config())
	_, _ = f.Push("Done! " + strings.Repeat("가", 301))
	_, err := f.Record(t.Context(), ledger.New(crashCanon{db, os.Getenv("LINA_REPLY_CRASH_PHASE")}, nil), recording())
	t.Fatalf("crash boundary not reached: %v", err)
}
func TestReplyAndCardCrashAtomicity(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestRecordingCrashChild$")
			cmd.Env = append(os.Environ(), "LINA_REPLY_CRASH_ROOT="+root, "LINA_REPLY_CRASH_PHASE="+phase)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 83 {
				t.Fatalf("child=%v %s", err, output)
			}
			store := ledger.New(crashDB(t, root, 2), nil)
			source, sourceErr := store.Get(t.Context(), "reply")
			card, cardErr := store.Get(t.Context(), "card-event")
			check, checkErr := store.Get(t.Context(), "reply-check:reply")
			if phase == "before" {
				if !errors.Is(sourceErr, ledger.ErrNotFound) || !errors.Is(cardErr, ledger.ErrNotFound) || !errors.Is(checkErr, ledger.ErrNotFound) {
					t.Fatalf("uncommitted pair: %v %v", sourceErr, cardErr)
				}
			} else if sourceErr != nil || cardErr != nil || checkErr != nil || card.ParentID != source.ID || check.ParentID != source.ID {
				t.Fatalf("crash stranded a reply: source=%v card=%v", sourceErr, cardErr)
			}
		})
	}
}
