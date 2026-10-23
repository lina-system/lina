// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
	"path/filepath"
)

func maintenanceService(t *testing.T, model func(context.Context, Call) (Result, error)) *Service {
	t.Helper()
	s, _, _ := fixture(t)
	d := SkillMaintenanceDefinition(nil, model)
	d.Agent, d.Account, d.Provider = "main", "account", provider.ChatGPT
	s.cfg.Definitions[SkillMaintenanceJob] = d
	if err := s.Ensure(t.Context(), jobapi.Job{ID: SkillMaintenanceJob, Kind: jobapi.Planning, Enabled: true, Every: time.Hour, Timeout: time.Second, Budget: 100, MaxTokens: 10, Adoption: "baseline"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSkillMaintenanceAbandonsFailedReservation(t *testing.T) {
	for _, failure := range []string{"provider", "timeout", "invalid", "cancelled", "restart"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			callCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s := maintenanceService(t, func(context.Context, Call) (Result, error) {
				calls++
				if calls > 1 {
					return Result{Output: json.RawMessage(`{"actions":[]}`)}, nil
				}
				switch failure {
				case "timeout":
					return Result{}, context.DeadlineExceeded
				case "cancelled":
					cancel()
					return Result{Output: json.RawMessage(`{"actions":[]}`)}, nil
				case "invalid":
					return Result{Output: json.RawMessage(`not json`)}, nil
				default:
					return Result{}, errors.New("provider failed")
				}
			})
			first := json.RawMessage(`[{"revision":1}]`)
			next := json.RawMessage(`[{"revision":2}]`)
			if err := s.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, first); err != nil {
				t.Fatal(err)
			}
			req := jobapi.AuxRequest{Epoch: 1, JobID: SkillMaintenanceJob, Through: 1}
			if _, err := s.Aux(callCtx, req); err == nil {
				t.Fatal("expected model failure")
			}
			readReservation := func() budget.Row {
				t.Helper()
				var row budget.Row
				if err := s.cfg.Store.Read(t.Context(), func(tx *canon.Tx) error {
					var id string
					if err := tx.QueryRowContext(t.Context(), "SELECT reservation FROM moirai_aux WHERE job=? AND sequence=1", SkillMaintenanceJob).Scan(&id); err != nil {
						return err
					}
					var err error
					row, err = budget.Read(t.Context(), tx, id)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return row
			}
			before := readReservation()
			if failure == "restart" {
				var err error
				s, err = New(s.cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := s.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, first); err != nil {
				t.Fatal(err)
			}
			j, err := s.Policy(t.Context(), SkillMaintenanceJob)
			if err != nil || j.Watermark != 1 {
				t.Fatalf("failed pass still blocks later snapshots: %+v %v", j, err)
			}
			if _, err := s.Aux(t.Context(), req); !errors.Is(err, jobapi.ErrConflict) {
				t.Fatalf("abandoned call replay: %v", err)
			}
			if err := s.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, next); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: SkillMaintenanceJob, From: 1, Through: 2}); !errors.Is(err, jobapi.ErrNotDue) {
				t.Fatalf("lost interval: %v", err)
			}
			s.cfg.Now = func() time.Time { return j.NextRun }
			aux, err := s.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: SkillMaintenanceJob, From: 1, Through: 2})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: SkillMaintenanceJob, From: 1, Through: 2, Output: aux.Output}); err != nil {
				t.Fatal(err)
			}
			after := readReservation()
			if before.ID != after.ID || before.Charged != after.Charged || after.Settled || calls != 2 {
				t.Fatalf("replayed or released original reservation: before=%+v after=%+v calls=%d", before, after, calls)
			}
			if err := s.cfg.Store.Read(t.Context(), func(tx *canon.Tx) error {
				var input, output []byte
				if err := tx.QueryRowContext(t.Context(), "SELECT t.input,a.output FROM moirai_tasks t JOIN moirai_aux a USING(job,sequence) WHERE t.job=? AND t.sequence=1", SkillMaintenanceJob).Scan(&input, &output); err != nil {
					return err
				}
				if string(input) != string(first) || output != nil {
					t.Fatalf("abandoned task changed: %s %s", input, output)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSkillMaintenanceRetainsActiveAndCachedPass(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	s := maintenanceService(t, func(ctx context.Context, _ Call) (Result, error) {
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		return Result{Output: json.RawMessage(`{"actions":[]}`)}, nil
	})
	if err := s.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: SkillMaintenanceJob, Through: 1})
		done <- err
	}()
	<-started
	check := func() {
		t.Helper()
		if err := s.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, json.RawMessage(`[{"changed":true}]`)); err != nil {
			t.Fatal(err)
		}
		j, err := s.Policy(t.Context(), SkillMaintenanceJob)
		if err != nil || j.Watermark != 0 {
			t.Fatalf("consumed active or cached pass: %+v %v", j, err)
		}
	}
	check()
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	check()
	var err error
	s, err = New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	check()
}

func TestSkillMaintenanceJobFencesAndConsumesStale(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := skills.Open(root, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := "---\nname: old\ndescription: Old procedure.\n---\nThis procedure is obsolete."
	a, err := store.Patch(t.Context(), skills.Patch{ID: "old", Files: skills.Bundle{"SKILL.md": {Text: doc}}})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(SkillMaintenanceResult{Actions: []skills.Maintenance{{Kind: "prune", Target: a.Undo, Reason: "Replaced."}}})
	calls := 0
	def := SkillMaintenanceDefinition(store, func(context.Context, Call) (Result, error) { calls++; return Result{Output: result}, nil })
	def.Agent = "main"
	def.Account = "account"
	def.Provider = provider.ChatGPT
	service, err := New(Config{Store: db, Epoch: 1, Limit: 100, Definitions: map[string]Definition{SkillMaintenanceJob: def}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Ensure(t.Context(), jobapi.Job{ID: SkillMaintenanceJob, Kind: jobapi.Planning, Enabled: true, Every: time.Nanosecond, Timeout: time.Second, Budget: 100, MaxTokens: 10, Adoption: "baseline"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.MaintenanceSnapshot(t.Context())
	raw, _ := json.Marshal(snapshot)
	if err = service.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, raw); err != nil {
		t.Fatal(err)
	}
	if err = service.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, raw); err != nil {
		t.Fatal(err)
	}
	p := jobapi.Proposal{Epoch: 1, JobID: SkillMaintenanceJob, From: 0, Through: 1, Output: result}
	if _, err = service.Apply(t.Context(), p); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("worker supplied uncomputed result: %v", err)
	}
	aux, err := service.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: SkillMaintenanceJob, Through: 1})
	if err != nil {
		t.Fatal(err)
	}
	p.Output = json.RawMessage(`{"actions":[]}`)
	if _, err = service.Apply(t.Context(), p); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("worker replaced model output: %v", err)
	}
	if _, err = store.Patch(t.Context(), skills.Patch{ID: "old", Expected: 1, Files: skills.Bundle{"SKILL.md": {Text: doc + "\nUpdated by person."}}}); err != nil {
		t.Fatal(err)
	}
	p.Output = aux.Output
	if _, err = service.Apply(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	catalog, _ := store.Catalog(t.Context())
	notices, _ := store.MaintenanceNotices(t.Context())
	if len(catalog) != 1 || catalog[0].Revision != 2 || len(notices) != 0 || calls != 1 {
		t.Fatalf("stale pass changed catalog: %+v notices=%+v calls=%d", catalog, notices, calls)
	}
	if err = service.SetEnabled(t.Context(), SkillMaintenanceJob, false); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.MaintenanceSnapshot(t.Context())
	raw, _ = json.Marshal(snapshot)
	if err = service.ScheduleSnapshot(t.Context(), SkillMaintenanceJob, raw); err != nil {
		t.Fatal(err)
	}
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM moirai_tasks").Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("disabled job scheduled: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
