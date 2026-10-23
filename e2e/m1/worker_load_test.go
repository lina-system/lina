// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

type workerLoad struct{ path string }

func newWorkerLoad(t *testing.T, fixture string) workerLoad {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("installed worker capacity requires Linux host sampling")
	}
	load := workerLoad{filepath.Join(fixture, "loadavg")}
	load.set(t, "0.00")
	return load
}

func (load workerLoad) set(t *testing.T, value string) {
	t.Helper()
	// Keep the inode: a bind mount must see subsequent observations too.
	if err := os.WriteFile(load.path, []byte(value+" "+value+" "+value+" 1/1 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func (load workerLoad) wrap(t *testing.T, p *process) {
	t.Helper()
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Fatal("installed worker load fixture requires bubblewrap:", err)
	}
	// Only this Core and its children see the fixture. Keep the real installed
	// binary, sampler and CPU affinity; no product flag or sampler override.
	pidfile := load.path + ".core-pid"
	// --dev-bind preserves /dev/null and other device semantics used by Git
	// and Core's own sandbox probe. The shell execs Core at the recorded PID;
	// graceful shutdown targets Core, while forced cleanup kills the launcher
	// and --die-with-parent reaps its child.
	wrapped := append([]string{bwrap, "--die-with-parent", "--dev-bind", "/", "/", "--ro-bind", load.path, "/proc/loadavg", "--", "/bin/sh", "-c", `printf '%s\n' "$$" > "$1"; shift; exec "$@"`, "worker-load", pidfile}, p.command.Args...)
	// Linux parent-death signals track the thread that forked the child.
	// A Go runtime thread can retire while the test process remains alive.
	// The shell stays as bwrap's single-threaded parent until its child exits.
	// Save stdin before the asynchronous list replaces it with /dev/null.
	p.command.Args = append([]string{"/bin/sh", "-c", `exec 3<&0; "$@" <&3 3<&- & child=$!; exec 3<&-; wait "$child"`, "worker-load-parent"}, wrapped...)
	p.command.Path = "/bin/sh"
	p.signal = func(signal os.Signal) error {
		raw, err := os.ReadFile(pidfile)
		if err != nil {
			return err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 {
			return fmt.Errorf("invalid fixture Core pid: %q", raw)
		}
		child, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		defer child.Release()
		return child.Signal(signal)
	}
}

func workerDispatchRecords(ctx context.Context, h *harness) ([]dispatch.Record, error) {
	db, err := canon.OpenReader(ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		return nil, err
	}
	defer db.Close(context.Background())
	var records []dispatch.Record
	err = db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM worker_dispatch ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var r dispatch.Record
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			records = append(records, r)
		}
		return rows.Err()
	})
	return records, err
}

func workerLaunches(fixture string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(fixture, "launches"))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(raw), err
}

func logWorkerDispatchState(t *testing.T, h *harness, fixture string) {
	t.Helper()
	// The scenario deadline may already have expired; diagnostics own a bound.
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	records, err := workerDispatchRecords(ctx, h)
	launches, launchErr := workerLaunches(fixture)
	t.Logf("dispatch diagnostics: records=%d read_error=%v launches=%q launch_error=%v", len(records), err, launches, launchErr)
	for _, r := range records {
		t.Logf("dispatch id=%s state=%s wait=%s attempts=%d worker=%s tried=%v code=%s", r.ID, r.State, r.Wait, len(r.Tried), r.Worker, r.Tried, r.Code)
	}
}

func waitForHostCapacity(t *testing.T, h *harness, fixture string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var last string
	for {
		records, err := workerDispatchRecords(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 1 {
			t.Fatalf("expected one admitted dispatch, got %d", len(records))
		}
		r := records[0]
		launches, err := workerLaunches(fixture)
		last = fmt.Sprintf("state=%s wait=%s attempts=%d launches=%q", r.State, r.Wait, len(r.Tried), launches)
		if err != nil || r.State != "queued" || len(r.Tried) != 0 || launches != "" {
			t.Fatalf("high load started work: %s err=%v", last, err)
		}
		if r.Wait == "host_capacity" {
			t.Log("high load:", last)
			return r.ID
		}
		select {
		case <-ctx.Done():
			t.Fatalf("host capacity wait missing: %s: %v", last, ctx.Err())
		case <-tick.C:
		}
	}
}

func assertCapacityWorkOnce(t *testing.T, h *harness, fixture, id string) {
	t.Helper()
	records, err := workerDispatchRecords(h.ctx, h)
	if err != nil || len(records) != 1 {
		t.Fatalf("capacity dispatch lost: count=%d err=%v", len(records), err)
	}
	r := records[0]
	launches, err := workerLaunches(fixture)
	if err != nil || r.ID != id || r.State != "finished" || r.Wait != "" || len(r.Tried) != 1 || launches != "worker\n" {
		t.Fatalf("capacity work did not finish once: id=%s want=%s state=%s wait=%s attempts=%d launches=%q err=%v", r.ID, id, r.State, r.Wait, len(r.Tried), launches, err)
	}
	t.Logf("capacity work finished once: id=%s attempts=%d launches=%q", id, len(r.Tried), launches)
}
