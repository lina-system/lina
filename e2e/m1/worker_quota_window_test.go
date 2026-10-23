// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestM2WorkerQuotaWindows(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/worker-quota-windows.json", false)
}

func TestM2WorkerQuotaPartialRecovery(t *testing.T) {
	t.Parallel()
	runCodexWorkTools(t, "../m2/codex-quota-recovery.json", "", func(h *harness, stage string, _ []protocol.Question) {
		if stage != "completed" && stage != "restarted" {
			return
		}
		db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close(context.Background())
		var raw []byte
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT rows FROM worker_quota WHERE worker='codex'").Scan(&raw)
		})
		var rows []provider.QuotaRow
		if err != nil || json.Unmarshal(raw, &rows) != nil {
			t.Fatalf("quota evidence: %s %v", raw, err)
		}
		if len(rows) != 2 || rows[0].State != provider.QuotaOK || rows[1].State != provider.QuotaOK || !rows[0].Observed.After(rows[1].Observed) {
			t.Fatalf("%s: partial recovery did not reach dispatch: %s", stage, raw)
		}
		if work.ChooseWorker([]work.WorkerCandidate{{Name: "codex", Slots: 1, Quota: rows}}, nil, nil, nil, time.Now()) != "codex" {
			t.Fatalf("%s: recovered worker remains unavailable", stage)
		}
	})
}

func logWorkerQuotaState(t *testing.T, h *harness) {
	db, err := canon.OpenReader(context.Background(), filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Log("quota diagnostics:", err)
		return
	}
	defer db.Close(context.Background())
	err = db.Read(context.Background(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(context.Background(), "SELECT record FROM worker_dispatch")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var r dispatch.Record
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			t.Logf("dispatch state=%s wait=%s worker=%s tried=%v code=%s", r.State, r.Wait, r.Worker, r.Tried, r.Code)
		}
		return rows.Err()
	})
	if err != nil {
		t.Log("quota diagnostics:", err)
	}
}

func assertWorkerQuotaWindows(t *testing.T, h *harness, fixture string) {
	t.Helper()
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
		return tx.QueryRowContext(h.ctx, "SELECT rows FROM worker_quota WHERE worker='codex'").Scan(&quota)
	})
	var record dispatch.Record
	var rows []provider.QuotaRow
	if err != nil || json.Unmarshal(raw, &record) != nil || json.Unmarshal(quota, &rows) != nil {
		t.Fatal("missing worker quota evidence", err)
	}
	if !slices.Equal(record.Tried, []string{"codex", "antigravity"}) || record.State != "finished" || record.PR == nil {
		t.Fatalf("quota stop did not produce replacement work: %+v", record)
	}
	if len(rows) != 2 || rows[0].Window != provider.FiveHour || rows[0].State != provider.QuotaBlocked || rows[0].ResetsAt == nil || rows[1].Window != provider.Weekly || rows[1].State != provider.QuotaOK || rows[1].UsedPct == nil || *rows[1].UsedPct != 25 || rows[1].ResetsAt == nil || !rows[1].ResetsAt.After(*rows[0].ResetsAt) {
		t.Fatalf("independent quota windows lost: %s", quota)
	}
	// Use the persisted rows at the reported boundary without sleeping for quota.
	candidates := []work.WorkerCandidate{{Name: "codex", Slots: 1, Quota: rows}}
	if work.ChooseWorker(candidates, nil, nil, nil, *rows[0].ResetsAt) != "codex" {
		t.Fatal("healthy weekly window still blocks the recovered worker")
	}
	first, err := os.ReadFile(filepath.Join(fixture, "second-packet"))
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.ReadFile(filepath.Join(fixture, "packet"))
	if err != nil || string(first) != string(replacement) {
		t.Fatal("quota handoff changed the task packet", err)
	}
}
