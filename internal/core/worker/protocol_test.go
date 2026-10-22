// SPDX-License-Identifier: Apache-2.0
package worker_test

import (
	"encoding/json"
	"os/exec"
	"testing"

	protocol "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func TestGeneratedProtocolKeepsWireValues(t *testing.T) {
	var notification protocol.ServerNotification
	raw := []byte(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed","items":[],"error":null}}}`)
	if err := json.Unmarshal(raw, &notification); err != nil {
		t.Fatal(err)
	}
	if notification.TurnCompleted == nil || notification.TurnCompleted.Params.Turn.Status != protocol.V2TurnStatusCompleted {
		t.Fatalf("lost completed turn: %+v", notification)
	}
	wire, err := json.Marshal(notification)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if got["method"] != "turn/completed" {
		t.Fatal(string(wire))
	}

	// Unknown future variants remain observable on unverified worker versions.
	raw = []byte(`{"method":"future/event","params":{"n":9007199254740993}}`)
	if err := json.Unmarshal(raw, &notification); err != nil {
		t.Fatal(err)
	}
	wire, err = json.Marshal(notification)
	if err != nil || string(wire) != string(raw) {
		t.Fatalf("unknown variant changed: %s %v", wire, err)
	}

	for _, id := range []string{`9007199254740993`, `"9007199254740993"`} {
		var request protocol.JSONRPCRequest
		if err := json.Unmarshal([]byte(`{"id":`+id+`,"method":"turn/start","params":{}}`), &request); err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(request.ID)
		if err != nil || string(wire) != id {
			t.Fatalf("request id changed: %s %v", wire, err)
		}
	}
}

func TestGeneratedRequestsOmitUnspecifiedWorkerSettings(t *testing.T) {
	cwd := "/work/task"
	wire, err := json.Marshal(protocol.V2ThreadStartParams{Cwd: &cwd})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"approvalPolicy", "sandbox", "config", "model", "modelProvider", "baseInstructions"} {
		if _, exists := fields[key]; exists {
			t.Fatalf("unrequested %s emitted: %s", key, wire)
		}
	}
}

func TestGeneratedProtocolIsReproducible(t *testing.T) {
	cmd := exec.Command("node", "../../../third_party/codex-app-server/generate.mjs", "--check")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
