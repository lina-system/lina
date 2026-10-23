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

func TestM2DeliveryInstalledCore(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/delivery.json", false)
}

func assertDelivery(t *testing.T, h *harness, fixture, mode string) {
	t.Helper()
	db, close, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	var raw []byte
	var d dispatch.Record
	var card work.Card
	if err := db.QueryRow("SELECT record FROM worker_dispatch").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT record FROM work_cards WHERE task_id=?", d.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatal(err)
	}
	if card.Receipt == nil || card.Active || card.Run == nil || d.PR == nil || card.Head != d.PR.Head {
		t.Fatalf("no settled delivery: %+v", card)
	}
	launches, err := os.ReadFile(filepath.Join(fixture, "launches"))
	if err != nil {
		t.Fatal(err)
	}
	wantGenerations := 1
	if mode == "corrected" || mode == "residual" {
		wantGenerations = 2
		packet, err := os.ReadFile(filepath.Join(fixture, "return-packet"))
		if err != nil || !strings.Contains(string(packet), `"id":"major"`) || strings.Contains(string(packet), `"id":"minor"`) || strings.Contains(string(packet), `"id":"nit"`) || d.ReturnReceipt == "" {
			t.Fatalf("return did not filter by id: %s %v", packet, err)
		}
		var retained int
		if err := db.QueryRow("SELECT count(*) FROM work_reviews WHERE json_array_length(json_extract(record,'$.findings'))=3").Scan(&retained); err != nil || retained < 1 {
			t.Fatal("low priority findings lost", retained, err)
		}
	}
	if strings.Count(string(launches), "worker\n") != wantGenerations || card.Run.Generation != wantGenerations {
		t.Fatalf("repeated or missing return: launches=%s run=%+v", launches, card.Run)
	}
	if mode == "corrected" {
		merges, err := os.ReadFile(filepath.Join(fixture, "merges"))
		if err != nil || string(merges) != card.Head+"\n" || !card.Applied || card.State != "done" || card.Verdict != "verified" {
			t.Fatalf("no confirmed merge: %+v merges=%s err=%v", card, merges, err)
		}
		if _, err := os.Stat(filepath.Join(fixture, "closes")); !os.IsNotExist(err) {
			t.Fatal("corrected PR closed")
		}
	} else {
		reason, err := os.ReadFile(filepath.Join(fixture, "closes"))
		if err != nil || card.Applied || card.Verdict != "" || card.State != "input_needed" {
			t.Fatalf("failure did not close and block: %+v reason=%s err=%v", card, reason, err)
		}
		if mode == "residual" && (!strings.Contains(string(reason), "P1 major") || strings.Contains(string(reason), "minor") || strings.Contains(string(reason), "nit")) {
			t.Fatal("wrong close findings", string(reason))
		}
		if mode == "residual-review" && !strings.Contains(string(reason), "residual-id") {
			t.Fatal("review reason lost", string(reason))
		}
		if mode == "failed-check" && !strings.Contains(string(reason), "foundation") {
			t.Fatal("check reason lost", string(reason))
		}
		if _, err := os.Stat(filepath.Join(fixture, "merges")); !os.IsNotExist(err) {
			t.Fatal("failed PR merged")
		}
		var record struct{ Delivery struct{ State string } }
		if err := db.QueryRow("SELECT record FROM coding_assignments WHERE id=?", d.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &record); err != nil || record.Delivery.State != "closed" {
			t.Fatal("missing close receipt", err)
		}
	}
}
