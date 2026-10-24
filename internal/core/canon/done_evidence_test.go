// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/kit/result"
)

func TestCompletionUpgradePreservesPublishedWorkReviews(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	// Version 48 is the published dev catalog, before completion evidence.
	cfg.Migrations = all[:48]
	db := openStore(t, cfg)
	review := `{"status":"pending","receipt":"retained"}`
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_reviews(repository,pr_number,head,record) VALUES(?,?,?,?)", "fixture/repo", 1, "reviewed-head", []byte(review))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	cfg.Migrations = all
	db = openStore(t, cfg)
	claim := result.Completion{ID: "upgraded-completion", AgentID: "agent", State: "claimed", Label: "not verified"}
	if err := db.Write(t.Context(), func(tx *Tx) error { return RecordDoneCompletion(t.Context(), tx, &claim) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var got []byte
		if err := tx.QueryRowContext(t.Context(), "SELECT record FROM work_reviews WHERE repository=? AND pr_number=? AND head=?", "fixture/repo", 1, "reviewed-head").Scan(&got); err != nil {
			return err
		}
		if string(got) != review {
			t.Fatalf("upgrade changed review: %s", got)
		}
		var breaches int
		if err := tx.QueryRowContext(t.Context(), "SELECT breaches FROM agent_done_breaches WHERE agent_id=?", claim.AgentID).Scan(&breaches); err != nil {
			return err
		}
		if breaches != 1 {
			t.Fatalf("completion lost after reopen: breaches=%d", breaches)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionCounterIsAtomicAndPerAgent(t *testing.T) {
	t.Parallel()
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
