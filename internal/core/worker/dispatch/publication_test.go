// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestQueuedCancelSettlesBeforePublicationAuthority(t *testing.T) {
	db := store(t)
	record := Record{ID: "published-work", State: "queued", Canceled: true}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", record.ID, "turn", "repo", record.State, mustJSON(record))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	s := &Service{ctx: t.Context(), cfg: Config{DB: db, AuthorizeExecution: func(context.Context, Record) (bool, error) { checks++; return false, nil }}, running: map[string]bool{}}
	s.sync()
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	if got := records[0]; got.State != "finished" || got.Code != "cancelled" || got.Attempt != 0 || checks != 0 {
		t.Fatalf("never-launched cancellation became uncertain: %+v authority checks=%d", got, checks)
	}
}
