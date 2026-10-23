// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/exchange"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/kit/loop"
)

func result(t *testing.T, item lastItem, out any) {
	t.Helper()
	var r loop.Result
	if err := json.Unmarshal([]byte(item.Output), &r); err != nil || r.Code != loop.OK {
		t.Errorf("tool %s: %s (%v)", item.CallID, item.Output, err)
		return
	}
	if out != nil {
		if err := json.Unmarshal(r.Output, out); err != nil {
			t.Error(err)
		}
	}
}

func TestHandoffReceiptsAndSingleArtifactOwner(t *testing.T) {
	var mu sync.Mutex
	var doc coreexec.RecordReceipt
	var owned exchange.ChangeResult
	var routed struct {
		Handoff exchange.Status
		Changed bool
	}
	received := make(chan struct{}, 1)
	allow := make(chan struct{})
	var allowOnce sync.Once
	defer allowOnce.Do(func() { close(allow) })
	var receiverCalls, checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := decodeRequest(t, r)
		item := last(q)
		receiver := strings.Contains(q.Instructions, "Return a short report of what you observed")
		if receiver {
			receiverCalls.Add(1)
			if item.CallID == "" {
				mu.Lock()
				artifact := "record:" + doc.ID
				mu.Unlock()
				visible := false
				for _, raw := range q.Input {
					if strings.Contains(string(raw), artifact) {
						visible = true
					}
				}
				if !visible {
					t.Error("receiver request omitted the permitted artifact identity")
				}
			}
			for _, raw := range q.Tools {
				var v struct{ Name string }
				_ = json.Unmarshal(raw, &v)
				if v.Name != "read_record" && v.Name != "write_record" {
					t.Errorf("receiver tool escaped artifact scope: %s", v.Name)
				}
			}
			switch item.CallID {
			case "receiver-read":
				var fresh coreexec.RecordReceipt
				result(t, item, &fresh)
				toolCall(w, "receiver-write", "write_record", coreexec.RecordWrite{ID: fresh.ID, Revision: fresh.Revision, Kind: "document", Title: "Draft", Note: "Updated by owning agent"})
			case "receiver-write":
				result(t, item, nil)
				reply(w, "The requested draft change is saved.")
			default:
				select {
				case received <- struct{}{}:
				default:
				}
				select {
				case <-allow:
				case <-r.Context().Done():
					return
				}
				mu.Lock()
				id := doc.ID
				mu.Unlock()
				toolCall(w, "receiver-read", "read_record", map[string]string{"id": id})
			}
			return
		}
		if item.Type == "function_call_output" {
			switch item.CallID {
			case "create":
				mu.Lock()
				result(t, item, &doc)
				id := doc.ID
				mu.Unlock()
				toolCall(w, "lease", "begin_artifact_change", map[string]any{"artifact": exchange.Artifact{Kind: "business_document", ID: "record:" + id}, "done": "Draft updated", "wanted": "Update draft", "out_of_scope": "No sending", "next_check": time.Now().Add(time.Hour), "deadline": time.Now().Add(2 * time.Hour)})
			case "lease":
				mu.Lock()
				result(t, item, &owned)
				mu.Unlock()
				reply(w, "Draft work is open.")
			case "edit":
				mu.Lock()
				result(t, item, &routed)
				mu.Unlock()
				reply(w, "The change request is pending.")
			case "verify":
				var fresh coreexec.RecordReceipt
				result(t, item, &fresh)
				if !strings.Contains(fresh.Markdown, "Updated by owning agent") {
					t.Error("check-back did not read the completed artifact")
				}
				checks.Add(1)
				reply(w, "The draft change is saved and checked.")
			default:
				t.Errorf("unexpected tool output: %s", item.CallID)
				reply(w, "Unexpected tool output.")
			}
			return
		}
		switch {
		case strings.Contains(item.Content, "Readiness barrier"):
			reply(w, "Ready.")
		case strings.Contains(item.Content, "Start owned draft"):
			toolCall(w, "create", "write_record", coreexec.RecordWrite{Kind: "document", Title: "Draft", Note: "Original"})
		case strings.Contains(item.Content, "Request another edit"):
			mu.Lock()
			d := doc
			mu.Unlock()
			toolCall(w, "edit", "write_record", coreexec.RecordWrite{ID: d.ID, Revision: d.Revision, Kind: "document", Title: "Draft", Note: "Second agent attempted edit"})
		case strings.Contains(item.Content, "Check the hand-off's end condition"):
			mu.Lock()
			id := doc.ID
			mu.Unlock()
			toolCall(w, "verify", "read_record", map[string]string{"id": id})
		default:
			t.Errorf("unexpected input: %s", item.Content)
			reply(w, "Unexpected input.")
		}
	}))
	t.Cleanup(server.Close)
	h := newCore(t, server.URL)
	lina, knowledge := h.agent("lina"), h.agent("knowledge")
	h.turn(lina, "open", "Start owned draft")
	mu.Lock()
	if owned.Change == nil {
		t.Fatalf("no open artifact work: %+v", owned)
	}
	mu.Unlock()
	h.turn(knowledge, "edit", "Request another edit")
	mu.Lock()
	status := routed.Handoff
	changed := routed.Changed
	d := doc
	mu.Unlock()
	if status.ID == "" || status.To != lina || changed {
		t.Fatalf("parallel write: %+v changed=%v", status, changed)
	}
	if status.Delivery != "pending" {
		t.Fatalf("admission was not pending: %+v", status)
	}
	select {
	case <-received:
	case <-h.ctx.Done():
		t.Fatal("receiver never ran")
	}
	h.read(func(tx *canon.Tx) error {
		var state, owner, revision string
		var n int
		if err := tx.QueryRowContext(h.ctx, `SELECT state FROM exch_msg WHERE delivery_id=?`, status.DeliveryID).Scan(&state); err != nil {
			return err
		}
		if state != "read" {
			t.Errorf("receiver lacks delivery receipt: %s", state)
		}
		if err := tx.QueryRowContext(h.ctx, `SELECT owner FROM artifact_changes WHERE artifact=?`, "record:"+d.ID).Scan(&owner); err != nil {
			return err
		}
		if owner != lina {
			t.Error("lease changed owner")
		}
		if err := tx.QueryRowContext(h.ctx, `SELECT count(*) FROM artifact_changes WHERE artifact=?`, "record:"+d.ID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("open changes=%d", n)
		}
		// No receiving model work has been allowed to run yet.
		if err := tx.QueryRowContext(h.ctx, `SELECT revision FROM records_index WHERE id=?`, d.ID).Scan(&revision); err != nil {
			return err
		}
		if revision != d.Revision {
			t.Error("second agent changed the artifact")
		}
		return nil
	})
	allowOnce.Do(func() { close(allow) })
	h.await(`SELECT count(*) FROM exch_notices WHERE delivered=1 AND reason='report'`, 1)
	if checks.Load() != 1 {
		t.Fatalf("result reports=%d", checks.Load())
	}
	before := receiverCalls.Load()
	h.stop()
	h.start()
	h.await(`SELECT count(*) FROM exch_notices WHERE delivered=1`, 1)
	h.turn(lina, "barrier", "Readiness barrier")
	if receiverCalls.Load() != before || checks.Load() != 1 {
		t.Fatalf("restart replayed receiver/check-back: %d/%d checks=%d", before, receiverCalls.Load(), checks.Load())
	}
	h.stop()
}

func TestHandoffNextCheckSurvivesRestart(t *testing.T) {
	var peer string
	var mu sync.Mutex
	var status exchange.Status
	var receiverCalls, checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := decodeRequest(t, r)
		item := last(q)
		if strings.Contains(q.Instructions, "Return a short report of what you observed") {
			receiverCalls.Add(1)
			http.Error(w, "synthetic unavailable", http.StatusServiceUnavailable)
			return
		}
		if item.Type == "function_call_output" {
			mu.Lock()
			result(t, item, &status)
			mu.Unlock()
			reply(w, "The hand-off is pending.")
			return
		}
		switch {
		case strings.Contains(item.Content, "Send delayed check"):
			toolCall(w, "send", "send_handoff", map[string]any{"to": peer, "handoff": exchange.HandOff{Subject: "Check a draft", Wanted: "Compare the draft", OutOfScope: "No edits", Done: "Draft checked", Check: "source", Deadline: time.Now().Add(24 * time.Hour)}, "next_check": time.Now().Add(time.Hour)})
		case strings.Contains(item.Content, "Check the hand-off's end condition"):
			if !strings.Contains(item.Content, "next_check") {
				t.Error("wrong check-back reason")
			}
			checks.Add(1)
			reply(w, "The receiver has not reported. The check is still pending.")
		default:
			reply(w, "Ready.")
		}
	}))
	t.Cleanup(server.Close)
	h := newCore(t, server.URL)
	peer = h.agent("knowledge")
	lina := h.agent("lina")
	h.turn(lina, "send", "Send delayed check")
	h.await(`SELECT count(*) FROM turn_runs WHERE id LIKE 'exchange-turn:%' AND state='failed'`, 1)
	mu.Lock()
	id := status.DeliveryID
	mu.Unlock()
	if id == "" {
		t.Fatal("no delivery")
	}
	h.stop()
	h.due(id)
	h.start()
	h.await(`SELECT count(*) FROM exch_notices WHERE reason='next_check' AND delivered=1`, 1)
	if checks.Load() != 1 || receiverCalls.Load() != 1 {
		t.Fatalf("check=%d receiver=%d", checks.Load(), receiverCalls.Load())
	}
	h.stop()
	h.start()
	h.turn(lina, "barrier", "Readiness barrier")
	h.await(`SELECT count(*) FROM exch_notices WHERE delivered=1`, 1)
	if checks.Load() != 1 || receiverCalls.Load() != 1 {
		t.Fatal("restart replayed accepted receiver or check-back")
	}
	h.stop()
}
