// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func TestDeterministicIngressCommitsWithReceiptAndRecoversDelivery(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := t.Context()
	if err := db.Write(ctx, func(tx *canon.Tx) error {
		j, err := readJob(ctx, tx, "memory")
		if err != nil {
			return err
		}
		j.Deterministic, j.Every = true, time.Nanosecond
		return saveJob(ctx, tx, j)
	}); err != nil {
		t.Fatal(err)
	}
	// Existing queued input takes the worker's path, and a lost post-commit
	// delivery is retried without adopting twice.
	delivery := errors.New("injected delivery failure")
	s.cfg.AfterApply = func(context.Context) error { return delivery }
	proposal := jobapi.Proposal{Epoch: 1, JobID: "memory", From: 0, Through: 1, Output: json.RawMessage(`{"fixture":1}`)}
	if _, err := s.Apply(ctx, proposal); !errors.Is(err, delivery) {
		t.Fatal(err)
	}
	s.cfg.AfterApply = nil
	if _, err := s.Apply(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	deliveryCalls := 0
	s.cfg.AfterApply = func(context.Context) error { deliveryCalls++; return delivery }
	if err := s.SubmitDeterministic(ctx, "memory", json.RawMessage(`{"fixture":2}`)); err != nil {
		t.Fatal(err)
	}
	if deliveryCalls != 1 {
		t.Fatalf("delivery attempts=%d", deliveryCalls)
	}
	s.cfg.AfterApply = nil
	if err := s.SetEnabled(ctx, "memory", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(ctx, jobapi.Job{ID: "memory", Kind: jobapi.Memory, Enabled: true, Deterministic: true, Every: time.Nanosecond, Timeout: time.Second, Adoption: "baseline"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SubmitDeterministic(ctx, "memory", json.RawMessage(`{"fixture":3}`)); !errors.Is(err, jobapi.ErrDisabled) {
		t.Fatalf("disabled job=%v", err)
	}
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		j, err := readJob(ctx, tx, "memory")
		if err != nil {
			return err
		}
		if j.Enabled || j.Watermark != 2 {
			t.Fatalf("reset durable job=%+v", j)
		}
		for _, table := range []string{"moirai_tasks", "moirai_applied", "accepted"} {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 2 {
				t.Fatalf("%s=%d", table, n)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
