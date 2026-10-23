// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestM2WorkAdmissionRestart(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/work-admission.json", false)
}

func readAdmission(t *testing.T, h *harness) (dispatch.Record, int) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var raw []byte
	var cards int
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch").Scan(&raw); err != nil {
			return err
		}
		return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM work_cards").Scan(&cards)
	})
	var record dispatch.Record
	if err != nil || json.Unmarshal(raw, &record) != nil {
		t.Fatal("missing dispatch admission", err)
	}
	return record, cards
}

func assertPendingDispatch(t *testing.T, h *harness, base string) {
	t.Helper()
	record, cards := readAdmission(t, h)
	if record.State != "admitting" || record.BaseCommit != base || cards != 0 {
		t.Fatalf("incomplete admission ran after restart: state=%s base=%s cards=%d", record.State, record.BaseCommit, cards)
	}
}

func assertDispatchResumed(t *testing.T, h *harness, base, owner string) {
	t.Helper()
	record, _ := readAdmission(t, h)
	if record.State == "admitting" || record.BaseCommit != base || record.Owner != owner || record.Manager == "" {
		t.Fatalf("retry failed to finish the pinned admission: state=%s base=%s owner=%s manager=%s", record.State, record.BaseCommit, record.Owner, record.Manager)
	}
}
