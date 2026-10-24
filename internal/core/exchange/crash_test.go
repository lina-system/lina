// SPDX-License-Identifier: Apache-2.0
package exchange_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/exchange"
)

func TestDeliverySurvivesSIGKILL(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("LINA_EXCHANGE_CRASH_ROOT"); root != "" {
		db := openAt(t, root, 1)
		s := exchange.New(db)
		r := start(t, s)
		mode := os.Getenv("LINA_EXCHANGE_CRASH_POINT")
		switch mode {
		case "queued":
		case "delivered":
			if _, err := s.Deliver(t.Context(), r.DeliveryID, now); err != nil {
				t.Fatal(err)
			}
		case "read":
			if _, err := s.Prepare(t.Context(), r.DeliveryID, "answerer", now, &knowledge{t: t}); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("bad crash point")
		}
		// Parent waits for this commit acknowledgement, then sends actual SIGKILL.
		fmt.Println("committed")
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		t.Fatal("child unexpectedly resumed")
	}
	for _, point := range []string{"queued", "delivered", "read"} {
		t.Run(point, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestDeliverySurvivesSIGKILL$")
			child.Env = []string{"LINA_EXCHANGE_CRASH_ROOT=" + root, "LINA_EXCHANGE_CRASH_POINT=" + point}
			var stderr bytes.Buffer
			child.Stderr = &stderr
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child.ProcessState == nil {
					child.Process.Kill()
					child.Wait()
				}
			})
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "committed" {
				t.Fatalf("commit marker: %v %s", scanner.Err(), stderr.String())
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = child.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child: %v %s", err, stderr.String())
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("not SIGKILL: %v", exit)
			}
			db := openAt(t, root, 2)
			s := exchange.New(db)
			r, err := s.Start(t.Context(), request(), now)
			if err != nil || r.Inserted {
				t.Fatalf("replayed request: %+v %v", r, err)
			}
			for range 3 {
				if _, err = s.Deliver(t.Context(), r.DeliveryID, now); err != nil {
					t.Fatal(err)
				}
			}
			var inputs, delivered int
			if err = db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='exchange'").Scan(&inputs); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM exch_event WHERE kind='delivered'").Scan(&delivered)
			}); err != nil || inputs != 1 || delivered != 1 {
				t.Fatalf("inputs=%d deliveries=%d err=%v", inputs, delivered, err)
			}
			k := &knowledge{t: t}
			_, err = s.Prepare(t.Context(), r.DeliveryID, "answerer", now, k)
			if point == "read" {
				if !errors.Is(err, exchange.ErrClaimed) || len(k.reads) != 0 {
					t.Fatalf("replayed accepted turn: %v %v", err, k.reads)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
