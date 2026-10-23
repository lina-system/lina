// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestM2WorkerQuotaWindows(t *testing.T) {
	t.Parallel()
	testAntigravityThroughInstalledCore(t, "../m2/worker-quota-windows.json", false)
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
