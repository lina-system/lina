// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"strings"
	"testing"
	"time"
)

func TestWorkerQuotaAvailableWithoutMutationOrExtraAuthority(t *testing.T) {
	in, err := workerSchema("worker_quota", json.RawMessage(`{"session":"thread"}`))
	if err != nil || in.Write {
		t.Fatalf("quota schema: %+v %v", in, err)
	}
	for _, raw := range []string{`{"session":"thread","person":"foreign"}`, `{"session":"thread","text":"approve"}`} {
		if _, err := workerSchema("worker_quota", json.RawMessage(raw)); err == nil {
			t.Fatal("quota accepted injected authority")
		}
	}
	found := false
	for _, declaration := range workerDeclarations() {
		var d struct{ Name string }
		if err := json.Unmarshal(declaration, &d); err != nil {
			t.Fatal(err)
		}
		found = found || d.Name == "worker_quota"
		if d.Name == "worker_answer" {
			t.Fatal("model can answer person approvals")
		}
	}
	if !found {
		t.Fatal("quota absent from running Core tool declarations")
	}
}

func TestWorkerProjectionAndReceiptRejectForeignScope(t *testing.T) {
	e := &workerSession{thread: worker.Thread{ID: "thread", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 3, BindingEpoch: 1}, packet: "explicit task only", workdir: "/work", origin: &conversationRuntime{main: "node"}}
	event := worker.Event{Thread: e.thread, Kind: worker.ToolCallEvent, ToolCall: &worker.ToolCall{ID: "request", CallID: "call", Namespace: "lina", Name: "read_projection", Arguments: json.RawMessage(`{}`)}}
	event.Thread.TurnID = "turn"
	result, err := e.executeWorkTool(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	projection := result.(map[string]any)
	if projection["task_packet"] != e.packet || projection["assignment_id"] != "task" || projection["revision"] == "" || len(projection["memory"].([]string)) != 0 || len(projection["materials"].([]string)) != 0 {
		t.Fatalf("scope: %+v", projection)
	}
	event.ToolCall.Arguments = json.RawMessage(`{"task":"other"}`)
	if _, err = e.executeWorkTool(t.Context(), event); err == nil {
		t.Fatal("caller enlarged projection scope")
	}
	event.ToolCall.Arguments = json.RawMessage(`{}`)
	event.Thread.TaskID = "other"
	if _, err = e.executeWorkTool(t.Context(), event); err == nil {
		t.Fatal("foreign task read projection")
	}
	event.Thread = e.thread
	event.Thread.TurnID = "turn"
	event.ToolCall.Name = "submit_receipt"
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: "foreign", ExecutionGeneration: 1, Attempt: 1, Outcome: "blocked_needs_input", Producer: "child", RevisionHash: strings.Repeat("0", 64), TurnRef: work.TurnRef{ThreadID: "thread", TurnID: "turn", TurnStatus: "completed"}, EmittedAt: time.Now()}
	receipt.EventID = work.ReceiptID(receipt)
	event.ToolCall.Arguments, _ = json.Marshal(map[string]any{"receipt": receipt})
	if _, err = e.executeWorkTool(t.Context(), event); err == nil {
		t.Fatal("foreign receipt persisted")
	}
}

func TestWorkerApprovalReplyPreservesPermissionRequestAndLimitsGrant(t *testing.T) {
	request := worker.Request{Method: "item/permissions/requestApproval", Params: json.RawMessage(`{"threadId":"t","turnId":"u","permissions":{"network":{"enabled":true},"fileSystem":{"write":["/work"]}}}`)}
	raw, err := approvalReply(request, authority.Once)
	if err != nil {
		t.Fatal(err)
	}
	var answer map[string]json.RawMessage
	json.Unmarshal(raw, &answer)
	if string(answer["scope"]) != `"turn"` || string(answer["permissions"]) != `{"fileSystem":{"write":["/work"]},"network":{"enabled":true}}` {
		t.Fatalf("changed permission scope: %s", raw)
	}
	raw, err = approvalReply(request, authority.Deny)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &answer)
	if string(answer["permissions"]) != `{}` {
		t.Fatalf("deny granted permissions: %s", raw)
	}
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval"} {
		raw, err = approvalReply(worker.Request{Method: method}, authority.Once)
		if err != nil || string(raw) != `{"decision":"accept"}` {
			t.Fatalf("approval %s: %s %v", method, raw, err)
		}
	}
}
