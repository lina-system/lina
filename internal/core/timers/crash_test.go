// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"bufio"
	"context"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/canon"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestClaimSIGKILL(t *testing.T) {
	at := instant("2026-01-01T12:00:00Z")
	if root := os.Getenv("LINA_TIMER_CRASH_ROOT"); root != "" {
		s, db := openTimers(t, root, 1)
		tm := timerFixture("crash", "reminder")
		tm.Schedule = Schedule{At: at}
		if _, err := s.Create(t.Context(), tm, at); err != nil {
			t.Fatal(err)
		}
		barrier := func() {
			fmt.Println("timer-claim-barrier")
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
			t.Fatal("barrier released without SIGKILL")
		}
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			fires, err := claimIn(t.Context(), tx, at, "1")
			if err != nil {
				return err
			}
			if len(fires) != 1 {
				t.Fatal("no fire", fires)
			}
			if os.Getenv("LINA_TIMER_CRASH_STAGE") == "before-commit" {
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
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run=^TestClaimSIGKILL$")
			child.Env = []string{"LINA_TIMER_CRASH_ROOT=" + root, "LINA_TIMER_CRASH_STAGE=" + stage}
			input, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
			scan := bufio.NewScanner(output)
			if !scan.Scan() || scan.Text() != "timer-claim-barrier" {
				t.Fatalf("no barrier: %s %v", scan.Text(), scan.Err())
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(); err == nil {
				t.Fatal("child survived")
			}
			s, db := openTimers(t, root, 2)
			var count int
			var next string
			err = db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM timer_fire").Scan(&count); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), "SELECT next_at FROM timer WHERE id='crash'").Scan(&next)
			})
			if err != nil {
				t.Fatal(err)
			}
			if stage == "before-commit" && (count != 0 || next != timestamp(at)) {
				t.Fatalf("partial claim: count=%d next=%s", count, next)
			}
			if stage == "after-commit" && (count != 1 || next != "") {
				t.Fatalf("partial claim: count=%d next=%s", count, next)
			}
			if err = s.Recover(t.Context(), "2"); err != nil {
				t.Fatal(err)
			}
			fires, err := s.ClaimDue(t.Context(), at, "2")
			if err != nil || len(fires) != 1 {
				t.Fatalf("lost fire: %+v %v", fires, err)
			}
			if err = s.Finish(t.Context(), fires[0]); err != nil {
				t.Fatal(err)
			}
			if err = s.Recover(t.Context(), "3"); err != nil {
				t.Fatal(err)
			}
			again, err := s.ClaimDue(t.Context(), at, "3")
			if err != nil || len(again) != 0 {
				t.Fatalf("duplicate: %+v %v", again, err)
			}
			status, err := s.Get(t.Context(), "crash", "person", "agent", "main:agent")
			if err != nil || len(status.Fires) != 1 || status.Fires[0].State != "handled" {
				t.Fatalf("fires=%+v err=%v", status, err)
			}
		})
	}
}
