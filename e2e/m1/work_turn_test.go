// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// A terminal failure must release the acceptance waiter, rather than consume
// the scenario deadline and starve unrelated workers in the same test shard.
func TestM2QueuedWorkTurnTerminal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, buildLina(t))
	core := h.startCore()
	h.connect(core).Close()
	h.stopCore(core)
	db, close := installedAcceptanceStore(t, h)
	defer close()
	write := func(query string, args ...any) {
		t.Helper()
		if err := db.Write(h.ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(h.ctx, query, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Completed turns in another conversation, from another source, or for an
	// earlier input must not settle this input, even while its turn is absent.
	for i, input := range []struct{ conversation, source, key string }{
		{"other", "client", "request"},
		{"conversation", "coding", "request"},
		{"conversation", "client", "earlier"},
		{"conversation", "client", "request"},
	} {
		write("INSERT INTO inbox(id,conversation,source,native_id,body) VALUES(?,?,?,?,?)", i+1, input.conversation, input.source, input.key, []byte("fixture"))
		if i < 3 {
			write("INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES(?,?,?,'completed')", input.source+input.key+input.conversation, input.conversation, i+1)
		}
	}
	if settled, err := queuedWorkTurnSettled(h.ctx, db, "conversation", "request"); settled || err != nil {
		t.Fatalf("another turn settled queued input: settled=%t err=%v", settled, err)
	}
	write("INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES('target','conversation',4,'active')")
	for _, state := range []string{"active", "completed", "failed", "aborted"} {
		t.Run(state, func(t *testing.T) {
			write("UPDATE turn_runs SET state=? WHERE id='target'", state)
			settled, err := queuedWorkTurnSettled(h.ctx, db, "conversation", "request")
			wantError := state == "failed" || state == "aborted"
			if settled != (state == "completed") || (err != nil) != wantError {
				t.Fatalf("state=%s settled=%t err=%v", state, settled, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if settled, err := queuedWorkTurnSettled(ctx, db, "conversation", "request"); settled || err == nil {
		t.Fatalf("canceled observation: settled=%t err=%v", settled, err)
	}
}
