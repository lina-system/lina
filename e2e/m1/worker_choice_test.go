// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/provider"
)

// TestM2WorkerChoice (m2/worker-choice.json) runs installed Core through the
// conversation protocol. Synthetic Antigravity and Codex streams prove unknown
// quota, blocked-worker exclusion, one quota handoff with identical packet
// bytes, a second quota stop, repository choice rules, and overlapping-area
// waits across restart. The worker creates the real result file and Git commit;
// GitHub observations are synthetic. The first quota-stopped process
// deliberately remains alive until Core reaps it, and the replacement checks
// that it is gone before touching the workspace.
func TestM2WorkerChoice(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/worker-choice.json", false)
}
func assertWorkerChoice(t *testing.T, h *harness, fixture, mode string) {
	t.Helper()
	if mode == "quota-windows" {
		assertWorkerQuotaWindows(t, h, fixture)
		return
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(fixture, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if mode == "rule" {
		if read("attempts") != "codex\n" {
			t.Fatal("repository choice rule ignored")
		}
		return
	}
	if mode == "blocked" || mode == "overlap" {
		want := "worker\n"
		if mode == "overlap" {
			want += "worker\n"
		}
		if got := read("launches"); got != want {
			t.Fatalf("extra worker launch: %q", got)
		}
		if mode == "overlap" {
			waitForAreaRelease(t, h)
		}
		return
	}
	if got := read("attempts"); got != "antigravity\ncodex\n" {
		t.Fatalf("replacement count/order: %q", got)
	}
	if read("first-packet") != read("second-packet") {
		t.Fatal("fallback changed the packet")
	}
	pid, err := strconv.Atoi(read("first-pid"))
	if err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("quota-stopped worker still alive")
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var raw, quota []byte
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch").Scan(&raw); err != nil {
			return err
		}
		return tx.QueryRowContext(h.ctx, "SELECT rows FROM worker_quota WHERE worker='antigravity'").Scan(&quota)
	})
	var record dispatch.Record
	var rows []provider.QuotaRow
	if err != nil || json.Unmarshal(raw, &record) != nil || json.Unmarshal(quota, &rows) != nil {
		t.Fatal("missing choice evidence", err)
	}
	if len(record.Tried) != 2 || record.Worker != "codex" || !record.AreaHeld || len(rows) != 1 || rows[0].State != provider.QuotaBlocked || rows[0].ResetsAt != nil {
		t.Fatalf("choice/lease/quota lost: %+v %+v", record, rows)
	}
	if mode == "quota-fallback" {
		result, err := os.ReadFile(filepath.Join(read("workdir"), "result.txt"))
		if err != nil || !strings.HasPrefix(string(result), "partial work\n") {
			t.Fatal("handoff discarded partial work", err)
		}
	}
	if mode != "double-quota" && !strings.Contains(read("workdir"), record.ID) {
		t.Fatal("fallback changed task workspace")
	}
}

// The first confirmed merge releases the queued task. Wait for both durable
// applications and notices so restart cannot race unfinished delivery.
func waitForAreaRelease(t *testing.T, h *harness) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var applied int
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, `SELECT count(*) FROM worker_dispatch d JOIN work_cards c ON d.id=c.task_id WHERE json_extract(c.record,'$.applied')=1 AND json_extract(d.record,'$.announced')=1`).Scan(&applied)
		})
		if err == nil && applied == 2 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("confirmed merge did not release queued work: applied=%d error=%v", applied, err)
		case <-tick.C:
		}
	}
}

func waitForAreaClaim(t *testing.T, h *harness, fixture string) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting, active int
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, `SELECT sum(state='queued' AND json_extract(record,'$.wait')='area'),sum(state IN ('running','finished')) FROM worker_dispatch`).Scan(&waiting, &active)
		})
		if err == nil && waiting == 1 && active == 1 {
			launches, err := os.ReadFile(filepath.Join(fixture, "launches"))
			if err == nil {
				if string(launches) != "worker\n" {
					t.Fatalf("overlap launched: %q", launches)
				}
				return
			}
			if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		select {
		case <-deadline.C:
			logWorkerQuotaState(t, h)
			t.Fatalf("area reservation not observed: waiting=%d active=%d err=%v", waiting, active, err)
		case <-tick.C:
		}
	}
}
