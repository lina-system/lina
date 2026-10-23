// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
)

// Other worker scenarios have no durable personal fact in their scripted
// turns. Acknowledge the independent extraction job without changing their
// planning or maintenance response sequence.
func answerEmptyMemoryExtraction(w http.ResponseWriter, input []json.RawMessage) bool {
	if len(input) != 2 {
		return false
	}
	var item struct{ Content string }
	var batch memory.ExtractionBatch
	if json.Unmarshal(input[1], &item) != nil || json.Unmarshal([]byte(item.Content), &batch) != nil || batch.Agent == "" || batch.Person == "" || len(batch.Utterances) == 0 {
		return false
	}
	emitResponse(w, "[]", true)
	return true
}

// The real installed Core admits the owner turn, schedules extraction, serves
// Aux/Apply to the worker binary, and persists the quoted memory across restart.
func TestM2InstalledMemoryExtraction(t *testing.T) {
	const statement = "I prefer jasmine tea."
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Tools) != 0 {
			emitResponse(w, "Understood.", true)
			return
		}
		calls.Add(1)
		if request.Instructions != "" || len(request.Input) != 2 {
			t.Error("extraction inherited persona or omitted its input")
			return
		}
		var input struct{ Content string }
		var batch memory.ExtractionBatch
		if json.Unmarshal(request.Input[1], &input) != nil || json.Unmarshal([]byte(input.Content), &batch) != nil {
			t.Error("invalid extraction input")
			return
		}
		if len(batch.Utterances) != 1 || batch.Utterances[0].Text != statement || len(batch.Utterances[0].Events) != 1 {
			t.Errorf("unexpected person evidence: %+v", batch)
			return
		}
		output, _ := json.Marshal([]memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea", Subject: memory.Subject{Kind: memory.Person, ID: batch.Person}, Basis: memory.Stated, Evidence: batch.Utterances[0].Events}})
		emitResponse(w, string(output), true)
	}))
	defer server.Close()
	lina, node, worker := buildLina(t), buildCommand(t, "lina-node"), buildCommand(t, "moirai-worker")
	h := newHarness(t, lina)
	bundle := makeInstallBundle(t, h.root, lina, node)
	h, start := installConversationBundle(t, h, bundle, server.URL)
	core := start(true)
	runWorker := func() {
		t.Helper()
		p := h.command("--canon", filepath.Join(h.root, "canon/canon.sqlite"), "--socket", filepath.Join(h.root, "run/jobs.sock"), "--once")
		p.command.Path, p.command.Args[0] = worker, worker
		p.start(t)
		p.wait(t, 0)
	}
	runWorker()
	if calls.Load() != 0 {
		t.Fatal("no person event still called extraction model")
	}
	client, conversation := conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "extraction-owner", statement)
	runWorker()
	if calls.Load() != 1 {
		t.Fatalf("owner statement extraction calls=%d, want 1", calls.Load())
	}
	client.Close()
	h.stopCore(core)
	var firstID string
	inspect := func() {
		withFileCanon(t, h, func(db *canon.Store) {
			var person, eventID string
			if err := db.Read(h.ctx, func(tx *canon.Tx) error {
				var tasks, receipts int
				id := "memory-extraction:" + strings.TrimPrefix(conversation, "main:")
				if err := tx.QueryRowContext(h.ctx, "SELECT (SELECT count(*) FROM moirai_tasks WHERE job=?),(SELECT count(*) FROM moirai_applied WHERE job=?)", id, id).Scan(&tasks, &receipts); err != nil {
					return err
				}
				if tasks != 1 || receipts != 1 {
					t.Errorf("duplicate or pending extraction after learning: tasks=%d receipts=%d", tasks, receipts)
				}
				return tx.QueryRowContext(h.ctx, "SELECT person_id,id FROM conv_event WHERE speaker_kind='person' AND body=?", statement).Scan(&person, &eventID)
			}); err != nil {
				t.Fatal(err)
			}
			hits, err := memory.New(db).Recall(h.ctx, memory.RecallScope{Agent: strings.TrimPrefix(conversation, "main:"), Person: person}, memory.Query{At: time.Now().Unix(), Text: "jasmine"})
			if err != nil || len(hits) != 1 {
				t.Fatalf("learned memory: %+v %v", hits, err)
			}
			item := hits[0]
			if item.Basis != memory.Stated || len(item.Evidence) != 1 || item.Evidence[0].ID != eventID || item.Evidence[0].Quote != statement {
				t.Fatalf("lost stated evidence: %+v", item)
			}
			if firstID != "" && firstID != item.ID {
				t.Fatal("restart duplicated memory")
			}
			firstID = item.ID
		})
	}
	inspect()
	core = start(true)
	runWorker()
	h.stopCore(core)
	inspect()
	if calls.Load() != 1 {
		t.Fatalf("restart replayed extracted evidence: %d", calls.Load())
	}
}
