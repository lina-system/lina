// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"path/filepath"
	"testing"
	"time"
)

func TestM2DispatchedWorkerCrashRecovery(t *testing.T) {
	testHeadlessWorkerThroughInstalledCore(t, "../m2/work-dispatch-recovery.json", false)
}
func waitDispatchedRecovery(t *testing.T, h *harness, ready func(dispatch.Record) bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var last dispatch.Record
	for {
		db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		err = db.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch").Scan(&raw)
		closeDB()
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &last); err != nil {
			t.Fatal(err)
		}
		if ready(last) {
			return
		}
		select {
		case <-h.ctx.Done():
			t.Fatalf("dispatch recovery: state=%s code=%s wait=%s: %v", last.State, last.Code, last.Wait, h.ctx.Err())
		case <-ticker.C:
		}
	}
}
