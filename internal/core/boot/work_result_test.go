// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestReportWorkRequiresObservedTermination(t *testing.T) {
	for _, state := range []string{"running", "unknown_after_restart"} {
		t.Run(state, func(t *testing.T) {
			r, _, agent, _ := managementFixture(t)
			r.agent, r.conversation = agent, "conversation"
			r.personaMu = &sync.Mutex{}
			r.personaGrants = map[string]grant.Grant{}
			r.activities = lifecycle.NewRegistry(nil)
			var err error
			r.workArea, err = work.OpenArea(r.root, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { r.workArea.Close(context.Background()) })
			r.assets, err = assets.Open(t.Context(), r.root, r.db)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { r.assets.Close() })
			writer, err := assets.OpenMaterialWriter(t.Context(), r.assets)
			if err != nil {
				t.Fatal(err)
			}
			materials := workMaterials{writer: writer, store: r.assets, person: r.person}
			r.work = work.NewWithArea(r.db, r.workArea, materials)
			r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: r.resolvePersonaGrant})
			if err != nil {
				t.Fatal(err)
			}
			bindings := map[string]loop.Tool{}
			if err = r.prepareWorkTools("manager-turn", bindings); err != nil {
				t.Fatal(err)
			}
			call := func(name, id string, args any) loop.Result {
				t.Helper()
				b, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				return bindings[name].Execute(t.Context(), provider.ToolCall{ID: id, Name: name, Arguments: string(b)})
			}
			prepared := call("prepare_work", "prepare", coreexec.PrepareWork{TaskID: "task-1", Title: "file result", Done: "verified file at destination", Destination: t.TempDir()})
			var workspace work.Workspace
			if prepared.Code != loop.OK || json.Unmarshal(prepared.Output, &workspace) != nil {
				t.Fatalf("prepare: %+v", prepared)
			}
			if err = os.Mkdir(filepath.Join(workspace.Path, "out"), 0700); err != nil {
				t.Fatal(err)
			}
			run := work.Run{ID: "run-1", TaskID: "task-1", Generation: 1, Worker: "stub", ThreadID: "thread-1", TurnID: "worker-turn", NodeID: "fixture"}
			if err = r.work.Start(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
			receipt := work.Receipt{SchemaVersion: 1, RelationshipID: run.TaskID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now}
			for _, name := range []string{"out/result.txt", "report.md"} {
				path, body := filepath.Join(workspace.Path, name), []byte("worker claims completion: "+name)
				if err = os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				hash := fmt.Sprintf("%x", sha256.Sum256(body))
				receipt.Manifest = append(receipt.Manifest, work.Artifact{Path: path, SHA256: hash})
				receipt.Provenance = append(receipt.Provenance, work.Provenance{ArtifactID: name, TaskID: run.TaskID, NodeID: run.NodeID, Path: path, SHA256: hash, ObservedAt: now})
			}
			receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
			receipt.EventID = work.ReceiptID(receipt)
			if state == "unknown_after_restart" {
				r.work = work.NewWithArea(r.db, r.workArea, materials)
				if err = r.work.Recover(t.Context(), "restart"); err != nil {
					t.Fatal(err)
				}
				if err = r.prepareWorkTools("restarted-manager-turn", bindings); err != nil {
					t.Fatal(err)
				}
			}
			before, err := r.work.Get(t.Context(), run.TaskID)
			if err != nil || !before.Active || state == "unknown_after_restart" && before.State != "unknown" {
				t.Fatalf("execution fixture: %+v %v", before, err)
			}
			args := coreexec.ReportWork{TaskID: run.TaskID, RunID: run.ID, Receipt: receipt}
			result := call("report_work_result", "unobserved-report", args)
			var problem struct {
				Code      string `json:"code"`
				Retryable bool   `json:"retryable"`
				Next      string `json:"next_action"`
			}
			if result.Code != loop.Failed || json.Unmarshal(result.Output, &problem) != nil || problem.Code != "worker_termination_unobserved" || !problem.Retryable || problem.Next != "wait_for_worker_termination_then_report_again" {
				t.Errorf("unobserved completion was not retryably refused: %s %s", result.Code, result.Output)
			}
			card, err := r.work.Get(t.Context(), run.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !card.Active || card.TurnStatus != "" || card.State != before.State {
				t.Errorf("receipt released or changed unobserved execution: active=%t turn=%q state=%s", card.Active, card.TurnStatus, card.State)
			}
			if card.Receipt != nil || card.FileSnapshot != nil || len(card.Materials) != 0 {
				t.Error("unobserved report published a receipt or snapshot")
			}
			var reserved int
			if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_active_runs WHERE task_id=? AND run_id=?", run.TaskID, run.ID).Scan(&reserved)
			}); err != nil || reserved != 1 {
				t.Errorf("reservation released: count=%d err=%v", reserved, err)
			}
			fingerprint := strings.Repeat("0", 64)
			if card.FileSnapshot != nil {
				fingerprint = card.FileSnapshot.Fingerprint
			}
			land := coreexec.LandWork{TaskID: run.TaskID, ReceiptID: receipt.EventID, Fingerprint: fingerprint, Judgment: "fixture judgment"}
			if result := call("land_work_result", "unobserved-land", land); result.Code == loop.OK {
				t.Error("landed while worker termination was unobserved")
			}
			next := run
			next.ID, next.Generation, next.TurnID = "run-2", 2, "worker-turn-2"
			if err = r.work.Start(t.Context(), next); !errors.Is(err, work.ErrActiveRun) {
				t.Errorf("new execution admitted before termination: %v", err)
			}
			if t.Failed() {
				return
			}
			// Only the trusted adapter observation path may append this terminal
			// event. The report tool receives the same receipt before and after it.
			if err = r.work.Finish(t.Context(), run.TaskID, run.ID, "completed"); err != nil {
				t.Fatal(err)
			}
			result = call("report_work_result", "observed-report", args)
			if result.Code != loop.OK || json.Unmarshal(result.Output, &card) != nil || card.Active || card.TurnStatus != "completed" || card.FileSnapshot == nil {
				t.Fatalf("observed completion not admitted: %s %s", result.Code, result.Output)
			}
			land.Fingerprint = card.FileSnapshot.Fingerprint
			if result := call("land_work_result", "observed-land", land); result.Code != loop.OK {
				t.Fatalf("observed result did not land: %s %s", result.Code, result.Output)
			}
		})
	}
}
