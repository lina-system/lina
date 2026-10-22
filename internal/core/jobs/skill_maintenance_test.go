// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
	"path/filepath"
)

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
