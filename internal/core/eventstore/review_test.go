// SPDX-License-Identifier: Apache-2.0
package eventstore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/kit/diag"
)

func TestReopenWithConsumedReserveOnFullDisk(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "open"
		if restart {
			name = "prepare and open"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			reserve, err := PrepareReserve(dir)
			if err != nil {
				t.Fatal(err)
			}
			s, _ := memoryStore()
			r := root(t, s, diag.References{TaskID: newID(t)})
			failure, err := diag.NewEmitter(s).Emit(t.Context(), diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.StorageFailed, ParentEventID: r.EventID}, Result: diag.Failure, NextAction: diag.ReportFailure, ErrorClass: diag.Storage})
			if err != nil {
				t.Fatal(err)
			}
			// Exercise SQL commit before reopening. The driver remains scripted;
			// this proves the reserve policy, not real SQLite crash durability.
			var steps []sqlStep
			var rows [][]driver.Value
			for _, event := range []diag.Event{r, failure} {
				record, _ := json.Marshal(event)
				steps = append(steps, sqlStep{op: "begin"}, sqlStep{op: "query", statement: "SELECT record", columns: []string{"record"}}, sqlStep{op: "exec", statement: "INSERT INTO events", args: []driver.Value{event.EventID.String(), string(record)}}, sqlStep{op: "commit"})
				rows = append(rows, []driver.Value{event.EventID.String(), string(record)})
			}
			steps = append(steps,
				sqlStep{op: "query", statement: "PRAGMA journal_mode=WAL", columns: []string{"journal_mode"}, rows: [][]driver.Value{{"wal"}}},
				sqlStep{op: "exec", statement: "PRAGMA synchronous=FULL"},
				sqlStep{op: "query", statement: "PRAGMA synchronous", columns: []string{"synchronous"}, rows: [][]driver.Value{{int64(2)}}},
				sqlStep{op: "exec", statement: "CREATE TABLE"},
				sqlStep{op: "query", statement: "SELECT event_id, record", columns: []string{"event_id", "record"}, rows: rows},
				sqlStep{op: "query", statement: "SELECT event_id, record", columns: []string{"event_id", "record"}, rows: rows},
			)
			db := scriptedDB(t, steps)
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			b := sqlBackend{conn}
			for _, event := range []diag.Event{r, failure} {
				if err := b.append(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			if err := reserve.Release(); err != nil {
				t.Fatal(err)
			}
			if reserve.Present() {
				t.Fatal("released reserve reported present")
			}
			full := func(*os.File) error { return &os.PathError{Op: "write", Path: "reserve", Err: syscall.ENOSPC} }
			if restart {
				if err := reserve.Close(); err != nil {
					t.Fatal(err)
				}
				reserve, err = prepareReserve(dir, full)
				if err != nil {
					t.Fatalf("consumed reserve blocked restart: %v", err)
				}
			} else {
				reserve.allocate = full
			}
			defer reserve.Close()
			opened, err := Open(t.Context(), db, reserve, func(error) bool { return false })
			if err != nil {
				t.Fatalf("consumed reserve blocked Open: %v", err)
			}
			defer opened.Close()
			chain, err := opened.Chain(t.Context(), failure.EventID)
			if err != nil || len(chain) != 2 || chain[0].EventID != r.EventID || chain[1].EventID != failure.EventID {
				t.Fatalf("committed diagnosis unavailable: %v %v", chain, err)
			}
			if reserve.Present() {
				t.Fatal("consumed reserve reported present after Open")
			}
			if err := reserve.Replenish(); !errors.Is(err, ErrStorage) || reserve.Present() {
				t.Fatalf("failed explicit allocation not reported as consumed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "system", "events", "reserve")); !os.IsNotExist(err) {
				t.Fatal("failed allocation left an apparent reserve")
			}
			reserve.allocate = allocateReserve
			if err := reserve.Replenish(); err != nil || !reserve.Present() {
				t.Fatalf("reserve not restored after space recovery: %v", err)
			}
		})
	}
}

func TestPrepareReserveStillRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			events := filepath.Join(dir, "system", "events")
			if err := os.MkdirAll(events, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(events, "reserve")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink("missing", path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "permissions":
				err = os.WriteFile(path, nil, 0644)
				if err == nil {
					err = os.Chmod(path, 0644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if r, err := prepareReserve(dir, func(*os.File) error { t.Fatal("unsafe file reached allocator"); return nil }); err == nil {
				r.Close()
				t.Fatal("unsafe reserve accepted")
			}
		})
	}
}

type checkingBackend struct {
	memoryBackend
	check func(context.Context)
}

func (b *checkingBackend) all(ctx context.Context) ([]diag.Event, error) {
	b.check(ctx)
	return b.memoryBackend.all(ctx)
}
func (b *checkingBackend) append(ctx context.Context, e diag.Event) error {
	b.check(ctx)
	return b.memoryBackend.append(ctx, e)
}

func TestCancelledContextRetainsTerminalDiagnosis(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, result := range []diag.Result{diag.Failure, diag.Unknown, diag.Denied, diag.Cancelled, diag.Pending, diag.Success} {
			label, _ := json.Marshal(result)
			path := "emit"
			if direct {
				path = "append"
			}
			t.Run(path+"/"+string(label), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					b := &checkingBackend{check: func(context.Context) {}}
					s := &Store{backend: b}
					r := root(t, s, diag.References{TaskID: newID(t)})
					type key struct{}
					running, stop := context.WithCancel(context.WithValue(context.Background(), key{}, 42))
					stop()
					terminal := result != diag.Pending && result != diag.Success
					b.check = func(ctx context.Context) {
						if !terminal {
							t.Fatal("cancelled ordinary event reached backend")
						}
						if err := ctx.Err(); err != nil {
							t.Fatalf("commit context cancelled: %v", err)
						}
						deadline, ok := ctx.Deadline()
						if !ok || deadline.After(time.Now().Add(5*time.Second)) {
							t.Fatal("commit context lacks a short bound")
						}
						if ctx.Value(key{}) != 42 {
							t.Fatal("context values lost")
						}
					}
					fields := diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.TaskStopped, ParentEventID: r.EventID}, Result: result, NextAction: diag.ReportFailure, ErrorClass: diag.Interrupted}
					var e diag.Event
					var err error
					if direct {
						e = diag.Event{EventID: newID(t), Time: time.Now().UTC(), Fields: fields}
						err = s.Append(running, e)
					} else {
						e, err = diag.NewEmitter(s).Emit(running, fields)
					}
					if !terminal {
						if !errors.Is(err, context.Canceled) || len(b.events) != 1 {
							t.Fatalf("ordinary result ignored cancellation: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("terminal diagnosis lost: %v", err)
					}
					b.check = func(context.Context) {}
					chain, err := s.Chain(context.Background(), e.EventID)
					if err != nil || len(chain) != 2 || chain[1].Result != result {
						t.Fatalf("terminal diagnosis absent from chain: %v %v", chain, err)
					}
				})
			})
		}
	}
}
