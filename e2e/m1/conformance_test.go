// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestM2ConformanceInstalledCore(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/conformance.json", false)
}
func assertConformance(t *testing.T, h *harness, mode string) {
	t.Helper()
	db, close, err := canon.OpenSQLiteInspection(h.ctx, h.root+"/canon/canon.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	var raw []byte
	var rr review.Record
	var card work.Card
	var d dispatch.Record
	if err = db.QueryRow("SELECT record FROM work_reviews ORDER BY json_extract(record,'$.generation') DESC LIMIT 1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &rr); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT record FROM work_cards WHERE task_id=?", rr.Task).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &card); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT record FROM worker_dispatch WHERE id=?", rr.Task).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if card.Receipt == nil || rr.Receipt != card.Receipt.EventID || rr.Head != card.Head || d.PR == nil || rr.Head != d.PR.Head || card.Applied != (mode == "success") {
		t.Fatal("review did not bind the actual delivery", rr, card)
	}
	if d.DesignText["design.md#result"] == "" || len(d.ChangedFiles) == 0 {
		t.Fatal("missing base design or observed files")
	}
	var managerInputs int
	if err = db.QueryRow("SELECT count(*) FROM inbox WHERE source='coding' AND native_id LIKE 'coding-delivery:%'").Scan(&managerInputs); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "scope":
		if rr.Verdict != "needs_changes" || card.Verdict != "" || card.State != "input_needed" || !strings.Contains(rr.Evidence, "outside.txt") || len(rr.Findings) != 0 || managerInputs != 0 {
			t.Fatalf("scope miss called model or lost evidence: %+v calls=%d", rr, managerInputs)
		}
	case "failed-check":
		if rr.State != "blocked" || rr.Cause != "merge_precondition" || rr.Verdict != "" || len(rr.Findings) != 0 || card.Verdict != "" || card.State != "input_needed" || managerInputs != 0 {
			t.Fatalf("failed check became judgment: %+v calls=%d card=%+v", rr, managerInputs, card)
		}
	case "prose":
		if rr.State != "unverified" || rr.Verdict != "" || card.Verdict != "" || len(rr.Findings) != 0 || managerInputs != 1 {
			t.Fatalf("prose stored judgment: %+v calls=%d", rr, managerInputs)
		}
	case "findings":
		if rr.State != "complete" || rr.Verdict != "needs_changes" || len(rr.Findings) != 1 || rr.Findings[0].Severity != "P1" || managerInputs != 2 || card.Verdict != "" || card.State != "input_needed" {
			t.Fatalf("structured result lost: %+v calls=%d", rr, managerInputs)
		}
	case "success":
		if rr.State != "complete" || rr.Verdict != "verified" || card.Verdict != "verified" || managerInputs != 1 {
			t.Fatalf("manager judgment missing: %+v calls=%d", rr, managerInputs)
		}
	}
}
