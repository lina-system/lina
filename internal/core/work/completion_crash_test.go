// SPDX-License-Identifier: Apache-2.0
package work

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func completionCrashStore(t *testing.T, root string, epoch uint64) (*Service, *canon.Store) {
	t.Helper()
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return New(db), db
}

func TestCompletionSIGKILLAtomicity(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("LINA_COMPLETION_CRASH_ROOT"); root != "" {
		s, db := completionCrashStore(t, root, 1)
		startFixture(t, s)
		if err := s.Submit(t.Context(), "run-1", receiptFixture()); err != nil {
			t.Fatal(err)
		}
		barrier := func() {
			fmt.Println("completion-barrier")
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
			t.Fatal("barrier released without SIGKILL")
		}
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			if err := FinishIn(t.Context(), tx, "task-1", "run-1", "completed", ""); err != nil {
				return err
			}
			if os.Getenv("LINA_COMPLETION_CRASH_STAGE") == "before-commit" {
				barrier()
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		barrier()
		return
	}
	for _, stage := range []string{"before-commit", "after-commit"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run=^TestCompletionSIGKILLAtomicity$")
			child.Env = []string{"LINA_COMPLETION_CRASH_ROOT=" + root, "LINA_COMPLETION_CRASH_STAGE=" + stage}
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "completion-barrier" {
				t.Fatalf("no checkpoint: %s %v", scanner.Text(), scanner.Err())
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(); err == nil {
				t.Fatal("child survived SIGKILL")
			}
			s, db := completionCrashStore(t, root, 2)
			card, err := s.Get(t.Context(), "task-1")
			if err != nil {
				t.Fatal(err)
			}
			var events, finishes int
			if err = db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_completions").Scan(&events); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_ledger WHERE event_key='finish:run-1'").Scan(&finishes)
			}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if stage == "after-commit" {
				want = 1
			}
			if events != want || finishes != want || card.Active != (want == 0) {
				t.Fatalf("partial commit: events=%d finishes=%d active=%v", events, finishes, card.Active)
			}
		})
	}
}
