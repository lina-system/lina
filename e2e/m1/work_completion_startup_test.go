// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
)

func TestM2PendingWorkCompletionAtStartup(t *testing.T) {
	t.Parallel()
	var scenario struct {
		Startup struct{ Title, Done, Reply string }
	}
	raw, err := os.ReadFile("../m2/work-completions.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var receiptID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(request.Input)
		if receiptID == "" || !strings.Contains(string(raw), receiptID) {
			t.Error("manager did not receive saved receipt")
		}
		calls.Add(1)
		emitResponse(w, scenario.Startup.Reply, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	person := c.Welcome.PersonID
	agent := strings.TrimPrefix(conversation, "main:")
	c.Close()
	h.stopCore(core)
	// Persist adapter-observed work while Core is stopped. This is a pending
	// completion at real startup, not a post-READY test-only callback.
	func() {
		ctl, err := ctlfiles.Open(h.root)
		if err != nil {
			t.Fatal(err)
		}
		defer ctl.Close()
		if err = ctl.Acquire(); err != nil {
			t.Fatal(err)
		}
		epoch, err := ctl.Epoch()
		if err != nil {
			t.Fatal(err)
		}
		db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close(context.Background())
		s := work.New(db)
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(s.Create(h.ctx, work.Task{ID: "pending-task", PersonID: person, AgentID: agent, ConversationID: conversation, Title: scenario.Startup.Title, Done: scenario.Startup.Done}))
		must(s.Start(h.ctx, work.Run{ID: "pending-attempt", TaskID: "pending-task", Generation: 1, Worker: "fixture", ThreadID: "thread", TurnID: "turn", NodeID: "node"}))
		receipt := work.Receipt{SchemaVersion: 1, RelationshipID: "pending-task", ExecutionGeneration: 1, Attempt: 1, RevisionHash: strings.Repeat("0", 64), Outcome: "blocked_needs_input", Producer: "child", TurnRef: work.TurnRef{ThreadID: "thread", TurnID: "turn", TurnStatus: "completed"}, EmittedAt: time.Now()}
		receipt.EventID = work.ReceiptID(receipt)
		receiptID = receipt.EventID
		must(s.Submit(h.ctx, "pending-attempt", receipt))
		must(s.Finish(h.ctx, "pending-task", "pending-attempt", "completed"))
	}()
	core = start(true)
	t.Cleanup(func() {
		if t.Failed() {
			_ = core.command.Process.Kill()
			<-core.done
			t.Logf("Core diagnostics: %s", core.stderr.String())
		}
	})
	waitCompletionHandled(t, h)
	if calls.Load() != 1 {
		t.Fatalf("pending completion was acknowledged without a manager turn: calls=%d", calls.Load())
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(h.ctx, `SELECT count(*) FROM inbox i JOIN turn_runs t ON t.inbox_id=i.id WHERE i.source='work' AND t.state='completed'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("completion bypassed durable conversation turn: %d", n)
		}
		return nil
	})
	db.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.stopCore(core)
	loseCompletion(t, h)
	core = start(true)
	waitCompletionHandled(t, h)
	h.stopCore(core)
	if calls.Load() != 1 {
		t.Fatal("restart replayed manager judgment")
	}
}
