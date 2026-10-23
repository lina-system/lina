// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	importer "github.com/thisisjun786/lina/internal/addons/importer/hermes"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

// The #34 harness runs the real credential-free worker against Core's Unix job
// protocol. The optional import host installs its schema and registered rules.
func TestM1StagedImportWorker(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("import-stages.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		JobID      string `json:"job_id"`
		Profile    string `json:"profile"`
		Agent      string `json:"agent"`
		Person     string `json:"person"`
		Events     int    `json:"expected_events"`
		References int    `json:"expected_references"`
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildCommand(t, "moirai-worker"))
	controls, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, kind := range []string{"lcm", "state"} {
		path := filepath.Join(h.root, kind+".sqlite")
		db, err := canon.OpenSQLite(h.ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		ddl, err := os.ReadFile("../../testdata/hermes/" + kind + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(h.ctx, string(ddl)); err != nil {
			t.Fatal(err)
		}
		if kind == "lcm" {
			if _, err = db.Exec("UPDATE host_uid_bindings SET lineage_key='home:synthetic-session'"); err != nil {
				t.Fatal(err)
			}
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		paths[kind] = path
	}
	req := importer.Request{BatchID: "synthetic-batch", Confirmed: true, Profiles: []importer.Profile{{ID: scenario.Profile, AgentID: scenario.Agent, PersonID: scenario.Person, StatePath: paths["state"], LCMPath: paths["lcm"], OwnerChannels: []importer.OwnerChannel{{SessionID: "synthetic-session", Source: "cli"}}}}}
	second := req.Profiles[0]
	second.ID += "-second"
	req.Profiles = append(req.Profiles, second)
	plan, err := importer.Prepare(h.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	rehearsal, err := plan.Rehearse()
	if err != nil {
		t.Fatal(err)
	}
	wantRehearsal, err := json.Marshal(rehearsal)
	if err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()

	path := filepath.Join(h.root, "canon.sqlite")
	store, err := canon.Open(h.ctx, canon.Config{Path: path, Epoch: 1, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	var logicalTime atomic.Int64
	// Keep due times in the past for the real worker's read-only snapshots.
	// Advancing Core's clock explicitly avoids waiting for the job interval.
	logicalTime.Store(time.Unix(1, 0).UnixNano())
	service, err := jobs.New(jobs.Config{Store: store, Epoch: 1, Now: func() time.Time { return time.Unix(0, logicalTime.Load()) }, Definitions: map[string]jobs.Definition{scenario.JobID: {Agent: scenario.Agent, Apply: plan.ApplyJob}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Register(h.ctx, jobapi.Job{ID: scenario.JobID, Kind: jobapi.Memory, Enabled: true, Deterministic: true, Every: time.Minute, Timeout: operationTimeout, Adoption: "confirmed-import"}); err != nil {
		t.Fatal(err)
	}
	tasks := plan.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("tasks=%d", len(tasks))
	}
	for _, task := range tasks {
		if err = service.Enqueue(h.ctx, scenario.JobID, task); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(h.root, "run", "jobs.sock")
	stop := startJobService(t, h.ctx, service, socket, controls)
	defer stop()
	// --once adopts one task. Each conversation must survive a separate worker.
	for i := range tasks {
		worker := h.command("--canon", path, "--socket", socket, "--once")
		worker.start(t)
		worker.wait(t, 0)
		if got := jobWatermark(t, store, scenario.JobID); got != int64(i+1) {
			t.Fatalf("after worker %d: watermark=%d", i+1, got)
		}
		logicalTime.Add(int64(time.Minute))
	}
	if got := jobWatermark(t, store, scenario.JobID); got != 2 {
		t.Fatalf("watermark=%d", got)
	}
	verified, err := plan.Verify(h.ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Rows != 2*scenario.References {
		t.Fatalf("references=%d", verified.Rows)
	}
	if err = store.Read(h.ctx, func(tx *canon.Tx) error {
		for table, want := range map[string]int{"import_event": 2 * scenario.Events, "conv_event": 0, "moirai_aux": 0, "memory_item": 0, "moirai_applied": 2, "import_rehearsal": 1} {
			var n int
			if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Errorf("%s count=%d want %d", table, n, want)
			}
		}
		var stored string
		if err := tx.QueryRowContext(h.ctx, "SELECT report FROM import_rehearsal WHERE batch_id=?", req.BatchID).Scan(&stored); err != nil {
			return err
		}
		if stored != string(wantRehearsal) {
			t.Error("worker adoption changed the rehearsed report")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A new preparation and process do not duplicate adopted rows or call a model.
	again, err := importer.Prepare(h.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	report, err := again.Import(h.ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if report.AddedRecords != 0 || report.AddedReferences != 0 {
		t.Fatalf("reimport added rows: %+v", report)
	}
	worker := h.command("--canon", path, "--socket", socket, "--once")
	worker.start(t)
	worker.wait(t, 0)
}
