// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIInitialAgentMetadataInstalledCore(t *testing.T) {
	t.Parallel()
	const name = "Renamed companion"
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Instructions string
			Input        []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var identity struct{ Name string }
		if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &identity); err != nil || identity.Name != name {
			t.Errorf("model received stale identity: %+v, %v", identity, err)
		}
		if calls.Add(1) == 1 {
			emitPersonaCall(w, "metadata-tool", "skill_index", map[string]any{})
			return
		}
		var output struct{ Type, Output string }
		if len(body.Input) == 0 || json.Unmarshal(body.Input[len(body.Input)-1], &output) != nil || output.Type != "function_call_output" || !strings.Contains(output.Output, "persona-change") {
			t.Error("renamed agent did not return the real skill inventory")
		}
		emitResponse(w, "Metadata fixture reply", true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "agents", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	agent := client.Welcome.Agents[0].AgentID
	// Core's Welcome was built before this rename. A fresh client must read
	// current metadata even when it has no previous selection in memory.
	op, renamed := "rename", name
	if _, err := client.Agent(h.ctx, protocol.AgentRequest{Operation: &op, AgentID: &agent, Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	frame, err := client.Receive(h.ctx)
	if err != nil || frame.Error != nil {
		t.Fatal("rename failed", err, frame.Error)
	}
	result, ok := frame.Payload.(protocol.AgentResult)
	if !ok || result.Agents == nil {
		t.Fatal("rename omitted agent metadata")
	}
	found := false
	for _, a := range *result.Agents {
		found = found || a.AgentID == agent && a.Name == name
	}
	if !found {
		t.Fatal("Core did not retain the renamed agent")
	}
	terminal, _ := h.startConversationTUI(100, 28, "TERM=xterm-256color", "LC_ALL=C")
	snapshot := waitComposerReady(t, h, terminal)
	rows := screenRows(snapshot)
	if !strings.Contains(rows[0], name) || !strings.Contains(rows[len(rows)-2], name) {
		t.Fatalf("fresh TUI used stale agent metadata: header=%q composer=%q; want %q", rows[0], rows[len(rows)-2], name)
	}
	h.keys(terminal, "Metadata fixture input\r")
	waitScreen(t, h, terminal, "Metadata fixture reply")
	if calls.Load() != 2 {
		t.Fatal("expected one tool call and its returned result")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	client.Close()
	h.stopCore(core)
}
