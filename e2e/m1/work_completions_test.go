// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/e2e/fixture"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2WorkCompletions(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/work-completions.json", false)
}

func assertWorkCompletion(t *testing.T, h *harness) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		var events, handled, inputs int
		if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM work_completions").Scan(&events); err != nil {
			return err
		}
		if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM work_completion_claims WHERE state='handled'").Scan(&handled); err != nil {
			return err
		}
		if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM inbox WHERE source='coding' AND native_id LIKE 'coding-delivery:%'").Scan(&inputs); err != nil {
			return err
		}
		if events != 1 || handled != 1 || inputs != 1 {
			t.Fatalf("completion events=%d handled=%d manager inputs=%d", events, handled, inputs)
		}
		var matching int
		if err := tx.QueryRowContext(h.ctx, `SELECT count(*) FROM work_completions e JOIN work_cards c ON c.task_id=e.task_id JOIN worker_dispatch d ON d.id=e.task_id WHERE e.attempt=json_extract(c.record,'$.run.id') AND e.head=json_extract(d.record,'$.pull_request.headRefOid') AND e.head=json_extract(c.record,'$.head') AND json_extract(e.record,'$.receipt_id')=json_extract(c.record,'$.receipt.eventId')`).Scan(&matching); err != nil {
			return err
		}
		if matching != 1 {
			t.Fatal("completion lost current attempt, head or receipt")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertClaimBeforeManager(t *testing.T, h *harness) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Error(err)
		return
	}
	defer db.Close(context.Background())
	var n int
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, `SELECT count(*) FROM work_completions e JOIN work_completion_claims c ON c.id=e.id WHERE c.state='claimed'`).Scan(&n)
	})
	if err != nil || n != 1 {
		t.Errorf("manager reacted before durable completion and claim: %d %v", n, err)
	}
}

func waitCompletionHandled(t *testing.T, h *harness) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var n int
		if err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM work_completion_claims WHERE state='handled'").Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			return
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("completion was not acknowledged", h.ctx.Err())
		case <-ticker.C:
		}
	}
}

// Fault injection only while the installed Core is stopped. The next real
// startup must recreate this signal once, preserving its handled tombstone.
func loseCompletion(t *testing.T, h *harness) {
	t.Helper()
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
	if err = db.Write(h.ctx, func(tx *canon.Tx) error { _, err := tx.ExecContext(h.ctx, "DELETE FROM work_completions"); return err }); err != nil {
		t.Fatal(err)
	}
}

// Script only the current completion input. A prior completion in conversation
// history must not consume a later person's tool request.
func scriptedWorkCompletion(w http.ResponseWriter, input []json.RawMessage) bool {
	var item struct{ Role, Content string }
	if json.Unmarshal(fixture.CurrentInput(input), &item) != nil || item.Role != "user" {
		return false
	}
	_, envelope, _ := strings.Cut(item.Content, "\n")
	var event struct{ Text string }
	if json.Unmarshal([]byte(envelope), &event) != nil {
		return false
	}
	_, body, _ := strings.Cut(event.Text, "\n")
	var data struct{ Kind string }
	if json.Unmarshal([]byte(body), &data) != nil || data.Kind != "work_completion" {
		return false
	}
	emitResponse(w, "The saved work is available for review.", true)
	return true
}

// Queue a manual request behind any completion turn and wait for that exact
// durable input's turn, so another turn's idle frame cannot settle this one.
// Failed and aborted turns fail the scenario immediately; waiting for them to
// complete would consume the shard's deadline without any possible progress.
func queuedWorkTurn(t *testing.T, h *harness, c *clientapi.Client, conversation, key, text string) {
	t.Helper()
	mode := protocol.InputModeQueue
	id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text, Mode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	for {
		e, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.ReplyTo != nil && *e.ReplyTo == id {
			if e.Error != nil {
				t.Fatal(e.Error)
			}
			break
		}
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		settled, err := queuedWorkTurnSettled(h.ctx, db, conversation, key)
		if err != nil {
			t.Fatal(err)
		}
		if settled {
			return
		}
		select {
		case <-ticker.C:
		case <-h.ctx.Done():
			t.Fatal("manual work turn did not settle", h.ctx.Err())
		}
	}
}

func queuedWorkTurnSettled(ctx context.Context, db *canon.Store, conversation, key string) (bool, error) {
	var id, state string
	err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT t.id,t.state FROM inbox i JOIN turn_runs t ON t.inbox_id=i.id WHERE i.conversation=? AND i.source='client' AND i.native_id=?`, conversation, key).Scan(&id, &state)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // The admitted input has not started a turn yet.
	}
	if err != nil {
		return false, err
	}
	switch state {
	case "active":
		return false, nil
	case "completed":
		return true, nil
	default:
		return false, fmt.Errorf("manual work input %q: turn %s ended in state %s", key, id, state)
	}
}
