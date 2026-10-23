// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestACPPromptErrorsThroughRunningCore(t *testing.T) {
	// Hold the error behind the harness's progress barrier, then either keep
	// serving requests or close stdout immediately after the complete reply.
	script := strings.Replace(acpAgentScript, "   const definition=JSON.parse", `   if(process.env.LINA_ACP_CASE.startsWith('rpc-error')) {
    const error={jsonrpc:'2.0',id:pending,error:{code:-32603,message:'private-prompt-detail',data:{detail:'private-error-data'}}};
    process.stdout.write(JSON.stringify(error)+'\n',()=>{if(process.env.LINA_ACP_CASE==='rpc-error-exit')process.exit(0)});
    return;
   }
   const definition=JSON.parse`, 1)
	scenarios := []acpScenario{{name: "lost-reply", worker: "gemini", binary: "gemini", mode: "exit"}}
	for _, mode := range []string{"rpc-error", "rpc-error-exit"} {
		scenarios = append(scenarios, acpScenario{
			name: mode, worker: "gemini", binary: "gemini", mode: mode, script: script,
			afterFinish: assertACPPromptErrorReceipt,
		})
	}
	runACPWorkersThroughRunningCore(t, scenarios)
}

func assertACPPromptErrorReceipt(t *testing.T, h *harness, _ string, record dispatch.Record) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	service := work.New(db)
	card, err := service.Get(h.ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if card.Active || card.TurnStatus != "failed" || card.Receipt == nil || card.Receipt.Outcome != "failed" {
		t.Fatalf("RPC reply did not settle the turn with a failed receipt: state=%s code=%s active=%v status=%s receipt=%+v", record.State, record.Code, card.Active, card.TurnStatus, card.Receipt)
	}
	if record.Code != "prompt_failed" || record.Snapshot == nil || record.Snapshot.Finish == nil || record.Snapshot.Finish.Failure != worker.WorkerFailure || record.Snapshot.Finish.Status != worker.Failed {
		t.Fatalf("terminal worker failure missing: code=%s snapshot=%+v", record.Code, record.Snapshot)
	}
	if card.Receipt.TurnRef.ThreadID != card.Run.ThreadID || card.Receipt.TurnRef.TurnID != card.Run.TurnID || card.Receipt.TurnRef.TurnStatus != "failed" || len(card.Receipt.Provenance) != 1 || card.Receipt.Provenance[0].ArtifactID != "adapter-derived:"+record.ID {
		t.Fatal("failed receipt lost its observed turn or adapter provenance")
	}
	ledger, err := service.Ledger(h.ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	finished := 0
	for _, event := range ledger {
		if event.Kind == "finished" {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("terminal error produced %d finished events", finished)
	}
	if len(card.Receipt.Manifest) != 1 {
		t.Fatal("failed receipt has no result artifact")
	}
	artifact, err := os.ReadFile(card.Receipt.Manifest[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		Record dispatch.Record
		Card   work.Card
		Ledger any
	}{record, card, ledger})
	if err != nil || strings.Contains(string(raw)+string(artifact), "private-") {
		t.Fatal("remote error detail retained in completion evidence", err)
	}
	// An observed error ends the turn, but cannot establish external effects.
	// Keep the existing recovery fence until those effects are reconciled.
	var reservations int
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM work_worker_reservations WHERE task_id=?", record.ID).Scan(&reservations)
	}); err != nil {
		t.Fatal(err)
	}
	if record.State != "unknown" || record.Snapshot.EffectsKnown || reservations == 0 {
		t.Fatal("prompt error falsely established effect safety")
	}
}
