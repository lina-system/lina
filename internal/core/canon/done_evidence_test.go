// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/kit/result"
)

func TestCompletionCounterIsAtomicAndPerAgent(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	claim := result.Completion{ID: "one", AgentID: "agent-a", State: "claimed", Label: "not verified"}
	abort := errors.New("producer failed")
	if err := db.Write(t.Context(), func(tx *Tx) error {
		if err := RecordDoneCompletion(t.Context(), tx, &claim); err != nil {
			return err
		}
		return abort
	}); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	var count int
	check := func(agent string, want int) {
		t.Helper()
		err := db.Read(t.Context(), func(tx *Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT coalesce((SELECT breaches FROM agent_done_breaches WHERE agent_id=?),0)", agent).Scan(&count)
		})
		if err != nil || count != want {
			t.Fatalf("%s counter %d want %d: %v", agent, count, want, err)
		}
	}
	check("agent-a", 0)
	for range 2 {
		if err := db.Write(t.Context(), func(tx *Tx) error { return RecordDoneCompletion(t.Context(), tx, &claim) }); err != nil {
			t.Fatal(err)
		}
	}
	check("agent-a", 1)
	check("agent-b", 0)
	other := claim
	other.ID, other.AgentID = "two", "agent-b"
	if err := db.Write(t.Context(), func(tx *Tx) error { return RecordDoneCompletion(t.Context(), tx, &other) }); err != nil {
		t.Fatal(err)
	}
	check("agent-a", 1)
	check("agent-b", 1)
	claim.State = "done"
	if err := db.Write(t.Context(), func(tx *Tx) error { return RecordDoneCompletion(t.Context(), tx, &claim) }); err == nil {
		t.Fatal("overwrote a prior completion")
	}
	check("agent-a", 1)
}
