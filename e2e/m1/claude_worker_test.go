// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

// TestM2ClaudeWorkerThroughInstalledCore (m2/claude-worker.json) runs
// dispatch_work with worker=claude through installed Core. A headless fixture
// worker writes and commits a real artifact, opens a synthetic PR and reports a
// final result while staying alive. Core must reap it, retain the result and PR
// in an adapter-derived receipt, and preserve the conversation result on
// restart. Other cases cover permission denial, exit zero without a result and
// pending PR checks. No personal account or GitHub write is used. Run with go
// test ./e2e/m1 -run '^TestM2ClaudeWorkerThroughInstalledCore$'.
func TestM2ClaudeWorkerThroughInstalledCore(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/claude-worker.json", false)
}

func assertClaudeReceipt(t *testing.T, h *harness, mode string) {
	t.Helper()
	if code := claudeUnknownCode(mode); code != "" {
		assertClaudeUnknownExecution(t, h, code)
		return
	}
	files, err := filepath.Glob(filepath.Join(h.root, "work", "runs", "*", "result.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("adapter evidence: %v %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		ID     string
		Result struct {
			SessionID string `json:"session_id"`
			Result    string
		}
		PR *struct {
			Head   string                    `json:"headRefOid"`
			Checks []struct{ Status string } `json:"statusCheckRollup"`
		} `json:"pull_request"`
	}
	if err = json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(h.ctx)
	card, err := work.New(db).Get(h.ctx, evidence.ID)
	if err != nil || card.Receipt == nil || card.Run == nil || card.Run.Worker != "claude" {
		t.Fatalf("Claude receipt/run missing: %+v %v", card, err)
	}
	if len(card.Receipt.Provenance) != 1 || !strings.HasPrefix(card.Receipt.Provenance[0].ArtifactID, "adapter-derived:") {
		t.Fatal("receipt is not adapter-derived", card.Receipt)
	}
	if mode == "success" {
		if evidence.Result.SessionID != "fixture" || evidence.Result.Result != "Work submitted." || evidence.PR == nil || len(evidence.PR.Head) != 40 || card.Receipt.Outcome != "ready_for_review" {
			t.Fatalf("receipt must retain result and PR: %s", raw)
		}
	} else if mode == "pending" {
		if card.Receipt.Outcome != "blocked_needs_input" || evidence.PR == nil || len(evidence.PR.Checks) != 1 || evidence.PR.Checks[0].Status != "IN_PROGRESS" || card.Verdict != "" || card.Applied {
			t.Fatal("pending evidence was lost or became accepted delivery")
		}
	} else if card.Receipt.Outcome != "failed" {
		t.Fatal("failed worker became delivery")
	}
}

// TestM2HeadlessWorkerRecovery runs m2/headless-worker-recovery.json through
// installed Core: two Claude dispatch attempts retain their receipt artifacts
// and produce a durable decision without a third launch after restart.
func TestM2HeadlessWorkerRecovery(t *testing.T) {
	testHeadlessWorkerThroughInstalledCore(t, "../m2/headless-worker-recovery.json", false)
}
func assertHeadlessRecovery(t *testing.T, h *harness) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(h.ctx)
	var raw []byte
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch").Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	var rec dispatch.Record
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.State != "decision" || rec.Attempt != 2 {
		t.Fatalf("headless recovery=%s", raw)
	}
	card, err := work.New(db).Get(h.ctx, rec.ID)
	if err != nil || card.Run == nil || card.Run.Generation != 2 || card.Receipt == nil {
		t.Fatalf("missing second receipt: %+v %v", card, err)
	}
	for _, name := range []string{"result.json", "result-2.json"} {
		if _, err = os.Stat(filepath.Join(h.root, "work", "runs", rec.ID, name)); err != nil {
			t.Fatal("prior receipt artifact lost", err)
		}
	}
	var decisions int
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM worker_recovery WHERE id=? AND json_extract(record,'$.State')='decision'", "dispatch:"+rec.ID).Scan(&decisions)
	}); err != nil || decisions != 1 {
		t.Fatalf("decision not durable: %d %v", decisions, err)
	}
}

// A missing or malformed result frame does not settle the execution.
// Reopen its durable state after Core stops to verify the switch fence remains.
func assertClaudeUnknownExecution(t *testing.T, h *harness, code string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(h.root, "work", "runs", "*", "result.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("missing worker result invented terminal evidence: %v %v", files, err)
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(h.ctx)
	var record dispatch.Record
	var reservations int
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch").Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM work_worker_reservations WHERE worker_id GLOB ?", record.ID+":*").Scan(&reservations)
	}); err != nil {
		t.Fatal(err)
	}
	card, err := work.New(db).Get(h.ctx, record.ID)
	if err != nil || record.State != "unknown" || record.Code != code || record.WorkerName() != "claude" || len(record.Result) != 0 || reservations != 5 || !card.Active || card.Run == nil || card.Run.Worker != "claude" || card.Receipt != nil {
		t.Fatalf("missing result lost durable uncertainty: record=%+v card=%+v reservations=%d err=%v", record, card, reservations, err)
	}
}

func claudeUnknownCode(mode string) string {
	switch mode {
	case "exit-zero":
		return "missing_result"
	case "invalid_message", "session_mismatch", "invalid_result", "invalid_init", "invalid_usage":
		return mode
	}
	return ""
}
