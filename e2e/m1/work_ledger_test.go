// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The #34 harness runs the real Core binary. Task facts enter through the work
// engine API, and boot must rebuild missing projections before serving clients.
func TestM2WorkLedger(t *testing.T) {
	t.Parallel()

	var scenario struct {
		TaskID              string `json:"task_id"`
		AgentID             string `json:"agent_id"`
		ConversationID      string `json:"conversation_id"`
		Title, Done, Output string
		ClaimState          string `json:"claim_state"`
		AcceptedState       string `json:"accepted_state"`
		RestartState        string `json:"restart_state"`
	}
	data, err := os.ReadFile("../m2/work-ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildLina(t))
	core := h.startCore()
	h.client(core)
	h.stopCore(core)
	path := filepath.Join(h.root, "canon", "canon.sqlite")
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	_, epoch, err := canon.Inspect(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: path, Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	service := work.New(db)
	dir := filepath.Join(h.root, "work", "tasks", scenario.TaskID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	task := work.Task{ID: scenario.TaskID, AgentID: scenario.AgentID, ConversationID: scenario.ConversationID, Title: scenario.Title, Done: scenario.Done, ArtifactRoots: []string{dir}}
	if err = service.Create(h.ctx, task); err != nil {
		t.Fatal(err)
	}
	run := work.Run{ID: "run-e2e", TaskID: task.ID, Generation: 1, Worker: "fixture", ThreadID: "thread-e2e", TurnID: "turn-e2e", NodeID: "node-e2e"}
	if err = service.Start(h.ctx, run); err != nil {
		t.Fatal(err)
	}
	second := run
	second.ID = "run-second"
	if err = service.Start(h.ctx, second); !errors.Is(err, work.ErrActiveRun) {
		t.Fatalf("second active run: %v", err)
	}
	outputPath := filepath.Join(dir, "report.txt")
	if err = os.WriteFile(outputPath, []byte(scenario.Output), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	hash := fmt.Sprintf("%x", sha256.Sum256(output))
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: task.ID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: outputPath, SHA256: hash}}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	if err = service.Submit(h.ctx, run.ID, receipt); err == nil {
		t.Fatal("changed artifact without provenance accepted")
	}
	receipt.Provenance = []work.Provenance{{ArtifactID: "artifact-e2e", TaskID: task.ID, NodeID: run.NodeID, Path: outputPath, SHA256: hash, ObservedAt: now}}
	if err = service.Submit(h.ctx, run.ID, receipt); err != nil {
		t.Fatal(err)
	}
	if err = service.Finish(h.ctx, task.ID, run.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	claim, err := service.Get(h.ctx, task.ID)
	if err != nil || claim.State != scenario.ClaimState {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	if string(output) != scenario.Output {
		t.Fatal("output failed done condition")
	}
	if err = service.Judge(h.ctx, task.ID, receipt.EventID, "verified", "fixture-content-check"); err != nil {
		t.Fatal(err)
	}
	live, err := service.Get(h.ctx, task.ID)
	if err != nil || live.State != scenario.AcceptedState {
		t.Fatalf("accepted: %+v %v", live, err)
	}
	events, err := service.Ledger(h.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := work.Project(events)
	if err != nil || !reflect.DeepEqual(live, projected) {
		t.Fatalf("live/replay differ: %v", err)
	}
	task.ID = "task-interrupted"
	if err = service.Create(h.ctx, task); err != nil {
		t.Fatal(err)
	}
	run.ID = "run-interrupted"
	run.TaskID = task.ID
	if err = service.Start(h.ctx, run); err != nil {
		t.Fatal(err)
	}
	if err = db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(h.ctx, "DELETE FROM work_cards; DELETE FROM work_active_runs")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err = ctl.Close(); err != nil {
		t.Fatal(err)
	}
	// Only production startup can restore these indexes; the test does not call
	// Rebuild or Recover on its behalf.
	core = h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL=http://127.0.0.1:1/v1", "LINA_RESPONSES_MODEL=fixture", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	h.connect(core).Close()
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "running", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !slices.Contains(client.Welcome.Capabilities, protocol.Capability{Name: "running", Version: "1.0"}) {
		t.Fatal("Core has no running capability")
	}
	id := "task:" + scenario.TaskID + ":1"
	action := "open"
	if _, err = client.Running(h.ctx, protocol.RunningGet{RunID: &id, Action: &action}); err != nil {
		t.Fatal(err)
	}
	frame, err := client.Receive(h.ctx)
	if err != nil || frame.Error != nil {
		t.Fatalf("open work card: %+v %v", frame, err)
	}
	result, ok := frame.Payload.(protocol.Running)
	if !ok || result.Text == nil || !strings.Contains(*result.Text, scenario.AcceptedState+": "+scenario.Title) || !strings.Contains(*result.Text, "worker_report") || !strings.Contains(*result.Text, "core_review") {
		t.Fatalf("missing live card evidence: %+v", result)
	}
	reader, err := canon.OpenReader(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	restored, err := work.New(reader).Get(h.ctx, scenario.TaskID)
	if err != nil || !reflect.DeepEqual(live, restored) {
		t.Fatalf("Core did not rebuild card: %+v %v", restored, err)
	}
	interrupted, err := work.New(reader).Get(h.ctx, "task-interrupted")
	if err != nil || interrupted.State != scenario.RestartState || !interrupted.Active {
		t.Fatalf("Core recovery: %+v %v", interrupted, err)
	}
	h.stopCore(core)
}
