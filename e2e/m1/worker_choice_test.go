// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	if mode == "blocked" || mode == "overlap" || mode == "capacity" {
		if got := read("launches"); got != "worker\n" {
			t.Fatalf("extra worker launch: %q", got)
		}
		if mode == "overlap" {
			waitForAreaClaim(t, h, fixture)
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
			t.Fatalf("area reservation not observed: waiting=%d active=%d err=%v", waiting, active, err)
		case <-tick.C:
		}
	}
}

// Capture state before cleanup even when the scenario's own deadline elapsed.
func dispatchedWorkerDiagnostic(h *harness, fixture string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	db, err := canon.OpenReader(ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		return err.Error()
	}
	defer db.Close(context.Background())
	var records []string
	err = db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM worker_dispatch")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var r dispatch.Record
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &r); err != nil {
				return err
			}
			records = append(records, fmt.Sprintf("state=%s wait=%s worker=%s attempts=%d code=%s", r.State, r.Wait, r.Worker, len(r.Tried), r.Code))
		}
		return rows.Err()
	})
	_, launched := os.Stat(filepath.Join(fixture, "launches"))
	return fmt.Sprintf("dispatch=%v launched=%t read_error=%v", records, launched == nil, err)
}

// Installed Core still reads the OS sample through MainHost. Give each fixture
// its own load observation so concurrent build jobs cannot decide its outcome.
func bindDispatchLoad(t *testing.T, p *process, loadFile string) func() *os.Process {
	t.Helper()
	binary, err := exec.LookPath("bwrap")
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(); writer.Close() })
	fd := 3 + len(p.command.ExtraFiles)
	p.command.ExtraFiles = append(p.command.ExtraFiles, writer)
	p.command.Path = binary
	p.command.Args = append([]string{binary, "--die-with-parent", "--info-fd", strconv.Itoa(fd), "--bind", "/", "/", "--dev-bind", "/dev", "/dev", "--ro-bind", loadFile, "/proc/loadavg", "--"}, p.command.Args...)
	return func() *os.Process {
		t.Helper()
		writer.Close()
		if err := reader.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var info struct {
			PID int `json:"child-pid"`
		}
		if err := json.NewDecoder(reader).Decode(&info); err != nil || info.PID <= 0 {
			t.Fatal("Core namespace did not report its process", err)
		}
		reader.Close()
		target, err := os.FindProcess(info.PID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { target.Release() })
		return target
	}
}

func waitForHostCapacity(t *testing.T, h *harness, fixture string) {
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
		var raw []byte
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch").Scan(&raw)
		})
		var r dispatch.Record
		if err == nil && json.Unmarshal(raw, &r) == nil && r.State == "queued" && r.Wait == "host_capacity" {
			if len(r.Tried) != 0 {
				t.Fatal("capacity wait spent a worker attempt")
			}
			if _, err = os.Stat(filepath.Join(fixture, "launches")); !os.IsNotExist(err) {
				t.Fatal("worker launched without capacity", err)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("capacity wait missing", dispatchedWorkerDiagnostic(h, fixture))
		case <-tick.C:
		}
	}
}
