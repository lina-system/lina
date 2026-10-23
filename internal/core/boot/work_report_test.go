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
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/authority"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestWorkReportRequiresObservedExit(t *testing.T) {
	for _, state := range []string{"no run", "active", "recovered unknown", "completed", "failed", "interrupted", "wrong run", "old generation", "other conversation"} {
		t.Run(state, func(t *testing.T) {
			r, _, agent, _ := managementFixture(t)
			r.agent, r.conversation = agent, "conversation"
			r.personaMu = &sync.Mutex{}
			r.personaGrants = map[string]grant.Grant{}
			r.activities = lifecycle.NewRegistry(nil)
			area, err := work.OpenArea(r.root, "")
			if err != nil {
				t.Fatal(err)
			}
			defer area.Close(context.Background())
			store, err := assets.Open(t.Context(), r.root, r.db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			writer, err := assets.OpenMaterialWriter(t.Context(), store)
			if err != nil {
				t.Fatal(err)
			}
			r.work = work.NewWithArea(r.db, area, workMaterials{writer: writer, store: store, person: r.person})
			r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: r.resolvePersonaGrant})
			if err != nil {
				t.Fatal(err)
			}
			bindings := map[string]loop.Tool{}
			if err = r.prepareWorkTools("turn", bindings); err != nil {
				t.Fatal(err)
			}
			invoke := func(id, name string, args any) loop.Result {
				t.Helper()
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				return bindings[name].Execute(t.Context(), provider.ToolCall{ID: id, Name: name, Arguments: string(raw)})
			}
			prepared := invoke("prepare", "prepare_work", coreexec.PrepareWork{TaskID: "file-task", Title: "Write a result", Done: "Result delivered", Destination: t.TempDir()})
			if prepared.Code != loop.OK {
				t.Fatalf("prepare: %+v", prepared)
			}
			var workspace work.Workspace
			if err = json.Unmarshal(prepared.Output, &workspace); err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(filepath.Join(workspace.Path, "out"), 0700); err != nil {
				t.Fatal(err)
			}
			run := work.Run{ID: "run-1", TaskID: workspace.TaskID, Generation: 1, Worker: "fixture", ThreadID: "thread-1", TurnID: "turn-1", NodeID: r.main}
			if state != "no run" {
				if err = r.work.Start(t.Context(), run); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
			receipt := work.Receipt{SchemaVersion: 1, RelationshipID: run.TaskID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now}
			for _, name := range []string{"out/result.txt", "report.md"} {
				path, body := filepath.Join(workspace.Path, name), []byte("checked fixture result")
				if err = os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				hash := fmt.Sprintf("%x", sha256.Sum256(body))
				receipt.Manifest = append(receipt.Manifest, work.Artifact{Path: path, SHA256: hash})
				receipt.Provenance = append(receipt.Provenance, work.Provenance{ArtifactID: name, TaskID: run.TaskID, NodeID: run.NodeID, Path: path, SHA256: hash, ObservedAt: now})
			}
			receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
			receipt.EventID = work.ReceiptID(receipt)
			args := coreexec.ReportWork{TaskID: run.TaskID, RunID: run.ID, Receipt: receipt}
			switch state {
			case "recovered unknown":
				err = r.work.Recover(t.Context(), "restarted")
			case "completed", "failed", "interrupted":
				err = r.work.Finish(t.Context(), run.TaskID, run.ID, state)
			case "wrong run", "old generation", "other conversation":
				err = r.work.Finish(t.Context(), run.TaskID, run.ID, "completed")
				if state == "wrong run" {
					args.RunID = "foreign-run"
				} else if state == "old generation" && err == nil {
					next := run
					next.ID, next.TurnID, next.Generation = "run-2", "turn-2", 2
					err = r.work.Start(t.Context(), next)
				} else if state == "other conversation" {
					r.conversation = "foreign-conversation"
					err = r.prepareWorkTools("turn", bindings)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := r.work.Ledger(t.Context(), run.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			result := invoke("report", "report_work_result", args)
			card, err := r.work.Get(t.Context(), run.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if state == "completed" {
				if result.Code != loop.OK || card.Active || card.TurnStatus != "completed" || card.FileSnapshot == nil || len(card.Materials) != 2 || card.Applied {
					t.Fatalf("observed completion not admitted: %+v card=%+v", result, card)
				}
				if replay := invoke("report-again", "report_work_result", args); replay.Code != loop.OK {
					t.Fatalf("completed report replay: %+v", replay)
				}
				return
			}
			if result.Code == loop.OK {
				t.Errorf("report admitted without a matching observed exit: state=%s active=%t terminal=%q", state, card.Active, card.TurnStatus)
			}
			after, err := r.work.Ledger(t.Context(), run.TaskID)
			if err != nil || !reflect.DeepEqual(before, after) || card.Receipt != nil || card.FileSnapshot != nil || len(card.Materials) != 0 {
				t.Errorf("refused report mutated work: card=%+v err=%v", card, err)
			}
			if state == "active" || state == "recovered unknown" {
				next := run
				next.ID, next.TurnID, next.Generation = "run-2", "turn-2", 2
				if err = r.work.Start(t.Context(), next); !errors.Is(err, work.ErrActiveRun) {
					t.Errorf("unobserved worker lost reservation: %v", err)
				}
			}
		})
	}
}
