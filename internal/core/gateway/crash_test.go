// SPDX-License-Identifier: Apache-2.0
package gateway_test

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
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
)

type beforeCommitDB struct {
	*canon.Store
	wait func()
}

func (db beforeCommitDB) Write(ctx context.Context, f func(*canon.Tx) error) error {
	return db.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := f(tx); err != nil {
			return err
		}
		db.wait()
		return nil
	})
}
func TestSIGKILLAdmissionAndSend(t *testing.T) {
	if root := os.Getenv("LINA_GATEWAY_CRASH_ROOT"); root != "" {
		db, s, _ := setupAt(t, root)
		wait := func() {
			fmt.Println("ready")
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
			t.Fatal("parent did not kill child")
		}
		phase := os.Getenv("LINA_GATEWAY_CRASH_PHASE")
		var storage channels.Canon = db
		if phase == "before_commit" {
			storage = beforeCommitDB{db, wait}
		}
		g := gateway.New(storage, func(ctx context.Context, d gateway.Delivery) error {
			if err := turn.New(db).Claim(ctx, "turn", d.Conversation, d.InboxID); err != nil {
				return err
			}
			if phase == "after_commit" {
				wait()
			}
			return nil
		})
		if err := g.Admit(t.Context(), event("1", "", "next")); err != nil {
			t.Fatal(err)
		}
		if phase == "in_flight" {
			enqueue(t, db, s, "one", "reply")
			l := lease.New(func() {})
			defer l.Release()
			_, err := gateway.NewQueue(db, &sender{hook: wait}).SendNext(t.Context(), l, chat, now)
			t.Fatal(err)
		}
		t.Fatal("missed crash boundary")
	}
	for _, phase := range []string{"before_commit", "after_commit", "in_flight"} {
		t.Run(phase, func(t *testing.T) {
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
			child := exec.CommandContext(ctx, executable, "-test.run=^TestSIGKILLAdmissionAndSend$")
			child.Env = []string{"LINA_GATEWAY_CRASH_ROOT=" + root, "LINA_GATEWAY_CRASH_PHASE=" + phase}
			var stderr bytes.Buffer
			child.Stderr = &stderr
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child.ProcessState == nil {
					child.Process.Kill()
					child.Wait()
				}
			})
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || line != "ready\n" {
				t.Fatalf("readiness=%q %v stderr=%s", line, err, stderr.String())
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = child.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("not SIGKILL: %v %s", err, stderr.String())
			}
			db := openDB(t, root, 2)
			s := channels.New(db)
			cursor, err := s.Cursor(t.Context(), bot)
			if err != nil {
				t.Fatal(err)
			}
			rows := scalar(t, db, "SELECT count(*) FROM inbox")
			if phase == "before_commit" {
				if rows != 0 || cursor != "" {
					t.Fatalf("partial transaction: %d %q", rows, cursor)
				}
			} else if rows != 1 || cursor != "next" {
				t.Fatalf("lost transaction: %d %q", rows, cursor)
			}
			if err = turn.New(db).Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			dispatched := 0
			g := gateway.New(db, func(ctx context.Context, d gateway.Delivery) error {
				dispatched++
				return turn.New(db).Claim(ctx, "new-turn", d.Conversation, d.InboxID)
			})
			for range 2 {
				if err = g.Admit(t.Context(), event("1", "", "next")); err != nil {
					t.Fatal(err)
				}
			}
			expected := 0
			if phase == "before_commit" {
				expected = 1
			}
			if dispatched != expected || scalar(t, db, "SELECT count(*) FROM inbox") != 1 || scalar(t, db, "SELECT count(*) FROM turn_runs") != 1 {
				t.Fatalf("replayed: %d", dispatched)
			}
			cursor, err = s.Cursor(t.Context(), bot)
			if err != nil || cursor != "next" {
				t.Fatal(cursor, err)
			}
			if phase != "before_commit" {
				r, err := turn.New(db).Read(t.Context(), "turn")
				if err != nil || r.State != turn.Aborted {
					t.Fatalf("%+v %v", r, err)
				}
			}
			if phase == "in_flight" {
				a := &sender{}
				q := gateway.NewQueue(db, a)
				if err = q.Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
				l := lease.New(func() {})
				defer l.Release()
				if sent, err := q.SendNext(t.Context(), l, chat, now); err != nil || sent || a.calls != 0 {
					t.Fatalf("resent uncertain effect: %v %v", sent, err)
				}
				m, err := s.Message(t.Context(), "one")
				if err != nil || m.State != channels.Unknown || m.LedgerRef != "reply-one" {
					t.Fatalf("%+v %v", m, err)
				}
			}
		})
	}
}
