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
	"github.com/thisisjun786/lina/kit/provider"
)

// MaxTokens is an admission reservation, not a cap on reported input+output.
// Verify the existing accounting policy, including recovery and other jobs.
func TestAuxOverspendPersistsAndBlocksNextAdmission(t *testing.T) {
	for _, limit := range []string{"job", "shared"} {
		for _, partial := range []bool{false, true} {
			name := limit + "/complete-usage"
			if partial {
				name = limit + "/partial-usage"
			}
			t.Run(name, func(t *testing.T) {
				s, store, _ := fixture(t)
				s.cfg.Limit = 1000
				if limit == "shared" {
					s.cfg.Limit = 100
					if err := s.apply(t.Context(), 1, func(tx *canon.Tx) error {
						j, err := readJob(t.Context(), tx, "memory")
						if err != nil {
							return err
						}
						j.Budget = 1000
						return saveJob(t.Context(), tx, j)
					}); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				d := s.cfg.Definitions["memory"]
				input, output := int64(140), int64(10)
				usage := provider.Usage{Input: &input, Output: &output}
				wantCharge := int64(150)
				if partial {
					usage.Output = nil
					wantCharge = input
				}
				d.Model = func(context.Context, Call) (Result, error) {
					calls++
					return Result{Output: json.RawMessage(`{"text":"remembered"}`), Usage: usage}, nil
				}
				s.cfg.Definitions["memory"] = d
				s.cfg.Definitions["other"] = d
				if err := s.Register(t.Context(), jobapi.Job{ID: "other", Kind: jobapi.Memory, Enabled: true, Every: time.Minute, Timeout: time.Second, Budget: 100, MaxTokens: 10, Adoption: "baseline"}); err != nil {
					t.Fatal(err)
				}
				if err := s.Enqueue(t.Context(), "other", jobapi.Task{Sequence: 1, Input: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
				req := jobapi.AuxRequest{Epoch: 1, JobID: "memory", Through: 1}
				aux, err := s.Aux(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Read(t.Context(), func(tx *canon.Tx) error {
					var reserved, charged, settled int64
					var raw string
					if err := tx.QueryRowContext(t.Context(), "SELECT reserved,charged,settled,usage FROM budget_usage WHERE run='memory'").Scan(&reserved, &charged, &settled, &raw); err != nil {
						return err
					}
					wantUsage, _ := json.Marshal(usage)
					if reserved != 10 || charged != wantCharge || settled != 1 || raw != string(wantUsage) {
						t.Fatalf("lost overspend: reserved=%d charged=%d settled=%d usage=%s", reserved, charged, settled, raw)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				s, err = New(s.cfg)
				if err != nil {
					t.Fatal(err)
				}
				cached, err := s.Aux(t.Context(), req)
				if err != nil || string(cached.Output) != string(aux.Output) || calls != 1 {
					t.Fatalf("overspend lost cached result: calls=%d err=%v", calls, err)
				}
				if _, err := s.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: cached.Output}); err != nil {
					t.Fatal(err)
				}
				if err := s.Enqueue(t.Context(), "memory", jobapi.Task{Sequence: 2, Input: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
				s.cfg.Now = func() time.Time { return time.Now().Add(time.Hour) }
				if _, err := s.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: "memory", From: 1, Through: 2}); !errors.Is(err, jobapi.ErrBudget) || calls != 1 {
					t.Fatalf("overspent job admitted: calls=%d err=%v", calls, err)
				}
				_, err = s.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: "other", Through: 1})
				if limit == "shared" {
					if !errors.Is(err, jobapi.ErrBudget) || calls != 1 {
						t.Fatalf("shared overspend admitted another job: calls=%d err=%v", calls, err)
					}
				} else if err != nil || calls != 2 {
					t.Fatalf("job overspend blocked independent allowance: calls=%d err=%v", calls, err)
				}
			})
		}
	}
}
