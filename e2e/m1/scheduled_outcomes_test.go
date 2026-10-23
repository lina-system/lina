// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

type scheduledOutcomeScenario map[string]struct {
	Status string `json:"status"`
	Output string `json:"output"`
}

// TestM3ScheduledOutcomes (m3/scheduled-outcomes.json) steers active scheduled
// workers through the owner protocol. They write and commit real artifacts,
// then report success or failure through final assistant text without
// submitting receipts. Core delivers both reports to the originating persona
// and restores their untrusted evidence and recorded snapshots after restart.
// Neither report verifies the task nor releases its dependent. The synthetic
// worker rejects a second turn start.
func TestM3ScheduledOutcomes(t *testing.T) {
	raw, err := os.ReadFile("../m3/scheduled-outcomes.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario scheduledOutcomeScenario
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	testM3DependencyRelease(t, nil, nil, "", "", dependencyReleaseOptions{outcomes: scenario})
}

func checkScheduledOutcomes(t *testing.T, h *harness, conversation string, ids []string) {
	t.Helper()
	controls, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "followup-fixture", []protocol.Capability{{Name: "running", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	for _, i := range []int{0, 2} {
		id, err := controls.Control(h.ctx, protocol.FollowUp{RunID: "task:" + ids[i] + ":1", ConversationID: &conversation, Text: "Report the outcome."})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		for {
			frame, err := controls.Receive(ctx)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			if frame.ReplyTo == nil || *frame.ReplyTo != id {
				continue
			}
			if frame.Error != nil {
				cancel()
				t.Fatalf("scheduled owner steering rejected: %+v", frame.Payload)
			}
			break
		}
		cancel()
	}
	// The persona's actual completed turns prove delivery; a committed worker
	// claim alone does not satisfy the conversation-result assertion.
	waitWorkerCanon(t, h, `SELECT count(*) FROM conv_event e JOIN conv_event p ON p.id=e.parent_id JOIN turn_runs t ON t.id='turn:'||p.id WHERE p.scaffold_kind='worker_completion' AND e.speaker_kind='agent' AND coalesce(e.scaffold_kind,'') NOT IN ('result','provider_item') AND t.state='completed'`, 2)
}

func respondScheduledOutcome(t *testing.T, w http.ResponseWriter, inputs []json.RawMessage, scenario scheduledOutcomeScenario) bool {
	t.Helper()
	if len(inputs) == 0 {
		return false
	}
	var last struct{ Content string }
	if json.Unmarshal(inputs[len(inputs)-1], &last) != nil || !strings.Contains(last.Content, `<external_evidence origin="worker-session:`) {
		return false
	}
	for _, report := range scenario {
		if strings.Contains(last.Content, report.Output) {
			emitResponse(w, "Worker report: "+report.Output, true)
			return true
		}
	}
	t.Error("scheduled follow-up lost the worker's final output")
	emitResponse(w, "Worker output missing.", true)
	return true
}

func checkScheduledOutcomeRecords(t *testing.T, h *harness, store *work.Service, conversation string, ids []string, scenario scheduledOutcomeScenario) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	for _, i := range []int{0, 2} {
		card, err := store.Get(h.ctx, ids[i])
		if err != nil {
			t.Fatal(err)
		}
		if card.Active || card.State == "done" || card.Receipt != nil || card.Verdict != "" || card.Scheduling.Landed {
			t.Fatalf("worker claim became verified completion: %+v", card)
		}
		key := "a"
		folder := "shared"
		if i == 2 {
			key, folder = "c", "independent"
		}
		want := scenario[key]
		if card.TurnStatus != want.Status {
			t.Fatalf("wrong terminal status: %q", card.TurnStatus)
		}
		bytes, err := os.ReadFile(filepath.Join(card.Task.Schedule.Workdir, folder, key+".txt"))
		if err != nil || string(bytes) != "verified fixture result\n" {
			t.Fatalf("worker never produced the artifact: %q %v", bytes, err)
		}
		started, err := os.ReadFile(filepath.Join(h.root, ids[i]+".started"))
		if err != nil || string(started) != "1" {
			t.Fatalf("worker was repeated: %q %v", started, err)
		}
		var count int
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, `SELECT count(*) FROM conv_event e JOIN inbox i ON i.native_id=e.id JOIN conv_event p ON p.id=e.parent_id WHERE e.scaffold_kind='worker_completion' AND i.source='worker-finish' AND e.conversation_id=? AND p.conversation_id=e.conversation_id AND e.agent_id=p.agent_id AND e.agent_id<>? AND p.id=? AND i.conversation=e.conversation_id`, conversation, card.Task.AgentID, card.Task.Schedule.InputID).Scan(&count)
		})
		if err != nil || count != 2 {
			t.Fatalf("origin lost or duplicated outcomes: count=%d err=%v", count, err)
		}
	}
	dependent, err := store.Get(h.ctx, ids[1])
	if err != nil || dependent.Run != nil {
		t.Fatalf("worker report released unmerged dependency: %+v %v", dependent, err)
	}
}

func recallScheduledOutcomes(t *testing.T, w http.ResponseWriter, inputs []json.RawMessage, scenario scheduledOutcomeScenario, ids []string) {
	t.Helper()
	raw, _ := json.Marshal(inputs)
	for _, want := range scenario {
		if !strings.Contains(string(raw), want.Output) {
			t.Errorf("restart lost worker evidence %q", want.Output)
		}
	}
	var last struct {
		Type, Output string
		CallID       string `json:"call_id"`
	}
	if len(inputs) == 0 || json.Unmarshal(inputs[len(inputs)-1], &last) != nil {
		t.Error("missing model input")
		emitResponse(w, "Missing input.", true)
		return
	}
	if last.Type != "function_call_output" {
		emitPersonaCall(w, "recall-a", "worker_snapshot", map[string]string{"session": ids[0]})
		return
	}
	key := "a"
	status := "done"
	if last.CallID == "recall-c" {
		key, status = "c", "failed"
	}
	var result struct {
		Code   string
		Output struct {
			Live           bool
			Status, Output string
		}
	}
	if err := json.Unmarshal([]byte(last.Output), &result); err != nil || result.Code != "ok" || result.Output.Live || result.Output.Status != status || result.Output.Output != scenario[key].Output {
		t.Errorf("recorded scheduled outcome: %s (%v)", last.Output, err)
	}
	if key == "a" {
		emitPersonaCall(w, "recall-c", "worker_snapshot", map[string]string{"session": ids[2]})
		return
	}
	emitResponse(w, "The worker reports are retained. Acceptance remains unverified.", true)
}
