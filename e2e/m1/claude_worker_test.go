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
)

func TestM2ClaudeWorkerThroughInstalledCore(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/claude-worker.json", false)
}

func assertClaudeReceipt(t *testing.T, h *harness, mode string) {
	t.Helper()
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
			Head string `json:"headRefOid"`
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
		if card.Receipt.Outcome != "blocked_needs_input" || evidence.PR != nil {
			t.Fatal("pending checks became delivery")
		}
	} else if card.Receipt.Outcome != "failed" {
		t.Fatal("failed worker became delivery")
	}
}
