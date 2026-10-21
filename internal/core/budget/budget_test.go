// SPDX-License-Identifier: Apache-2.0
package budget

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/provider"
)

func fixture(t *testing.T) (*canon.Store, canon.Config) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: []canon.Migration{{Version: 1, SQL: Schema}}}
	s, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s, cfg
}

func TestUnknownUsageSurvivesReopenForEveryRunKind(t *testing.T) {
	s, cfg := fixture(t)
	kinds := []provider.RunKind{provider.Turn, provider.Background, provider.BackgroundJob, provider.Worker, provider.WorkerSubthread, provider.Auxiliary, provider.Replay}
	for _, kind := range kinds {
		r := Reservation{ID: string(kind), Agent: "agent", Account: "account", Provider: provider.ChatGPT, Kind: kind, Run: "run", Tokens: 10}
		err := s.Write(t.Context(), func(tx *canon.Tx) error {
			if err := Reserve(t.Context(), tx, r, 100); err != nil {
				return err
			}
			return Settle(t.Context(), tx, r.ID, provider.Usage{})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	err = s.Read(t.Context(), func(tx *canon.Tx) error {
		for _, kind := range kinds {
			row, err := Read(t.Context(), tx, string(kind))
			if err != nil {
				return err
			}
			if !row.Settled || row.Charged != 10 || row.Usage.Input != nil || row.Usage.Output != nil || row.Usage.CostEstimate != nil || row.Agent != "agent" || row.Run != "run" || row.Account != "account" || row.Provider != provider.ChatGPT || row.Kind != kind {
				t.Fatalf("bad row: %+v", row)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReservationLimitSettlementAndRollback(t *testing.T) {
	s, _ := fixture(t)
	reserve := func(id string, n int64) error {
		return s.Write(t.Context(), func(tx *canon.Tx) error {
			return Reserve(t.Context(), tx, Reservation{ID: id, Agent: "a", Account: "c", Provider: provider.ChatGPT, Kind: provider.Turn, Run: "r", Tokens: n}, 10)
		})
	}
	if err := reserve("first", 10); err != nil {
		t.Fatal(err)
	}
	if err := reserve("blocked", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
	input, output := int64(2), int64(3)
	usage := provider.Usage{Input: &input, Output: &output}
	settle := func(u provider.Usage) error {
		return s.Write(t.Context(), func(tx *canon.Tx) error { return Settle(t.Context(), tx, "first", u) })
	}
	if err := settle(usage); err != nil {
		t.Fatal(err)
	}
	if err := settle(usage); err != nil {
		t.Fatal("idempotent settlement", err)
	}
	if err := settle(provider.Usage{}); !errors.Is(err, ErrSettled) {
		t.Fatal("rewrote settled usage", err)
	}
	if err := reserve("second", 5); err != nil {
		t.Fatal(err)
	}
	if err := reserve("blocked", 1); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if err := reserve("negative", -1); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestReserveAndSettleRollbackTogether(t *testing.T) {
	s, _ := fixture(t)
	aborted := errors.New("abort")
	err := s.Write(t.Context(), func(tx *canon.Tx) error {
		r := Reservation{ID: "rollback", Agent: "a", Account: "c", Provider: provider.ChatGPT, Kind: provider.Replay, Run: "r", Tokens: 10}
		if err := Reserve(t.Context(), tx, r, 10); err != nil {
			return err
		}
		if err := Settle(t.Context(), tx, r.ID, provider.Usage{}); err != nil {
			return err
		}
		return aborted
	})
	if !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	err = s.Read(t.Context(), func(tx *canon.Tx) error { _, err := Read(t.Context(), tx, "rollback"); return err })
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("aborted usage persisted", err)
	}
}

func TestUnknownPartialAndKnownZeroCharges(t *testing.T) {
	for _, tc := range []struct {
		name          string
		input, output *int64
		want          int64
	}{
		{"partial", new(int64(3)), nil, 10},
		{"larger_partial", new(int64(12)), nil, 12},
		{"reported_zero", new(int64(0)), new(int64(0)), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := fixture(t)
			err := s.Write(t.Context(), func(tx *canon.Tx) error {
				r := Reservation{ID: "call", Agent: "a", Account: "c", Provider: provider.ChatGPT, Kind: provider.Turn, Run: "r", Tokens: 10}
				if err := Reserve(t.Context(), tx, r, 10); err != nil {
					return err
				}
				return Settle(t.Context(), tx, r.ID, provider.Usage{Input: tc.input, Output: tc.output})
			})
			if err != nil {
				t.Fatal(err)
			}
			err = s.Read(t.Context(), func(tx *canon.Tx) error {
				r, err := Read(t.Context(), tx, "call")
				if r.Charged != tc.want {
					t.Fatalf("charged %d, want %d", r.Charged, tc.want)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
