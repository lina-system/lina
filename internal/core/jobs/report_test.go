// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func readReport(t *testing.T, store *canon.Store) jobapi.JobState {
	t.Helper()
	var raw []byte
	if err := store.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT record FROM moirai_reports WHERE job='memory'").Scan(&raw)
	}); err != nil {
		t.Fatalf("committed report missing: %v", err)
	}
	var state jobapi.JobState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestApplyCommitsReportBeforeReplyFailure(t *testing.T) {
	s, store, _ := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.cfg.AfterApply = func(context.Context) error {
		cancel() // Commit succeeded, but the worker can no longer report.
		return ctx.Err()
	}
	p := jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: json.RawMessage(`{"text":"remembered"}`)}
	if _, err := s.Apply(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-commit failure=%v", err)
	}
	want := jobapi.JobState{Epoch: 1, Watermark: 1, Status: "applied"}
	if got := readReport(t, store); got != want {
		t.Fatalf("report=%+v want=%+v", got, want)
	}
	// A delayed receipt replay must not replace a later report at this watermark.
	want.Status = "failed"
	if err := s.Report(t.Context(), "memory", want); err != nil {
		t.Fatal(err)
	}
	s.cfg.AfterApply = nil
	if _, err := s.Apply(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if got := readReport(t, store); got != want {
		t.Fatalf("replay overwrote report: %+v", got)
	}
}

func TestReportFailureRollsBackApply(t *testing.T) {
	s, store, _ := fixture(t)
	if err := store.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_report BEFORE INSERT ON moirai_reports BEGIN SELECT RAISE(ABORT, 'report storage failed'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: json.RawMessage(`{"text":"remembered"}`)}
	if _, err := s.Apply(t.Context(), p); err == nil {
		t.Fatal("Apply committed without its report")
	}
	if err := store.Read(t.Context(), func(tx *canon.Tx) error {
		j, err := readJob(t.Context(), tx, "memory")
		if err != nil {
			return err
		}
		if j.Watermark != 0 || !j.NextRun.IsZero() {
			t.Fatalf("progress survived rollback: %+v", j)
		}
		for _, table := range []string{"accepted", "moirai_applied", "moirai_reports"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatalf("%s survived rollback", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
