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
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUISavedAgentReopenInstalledCore(t *testing.T) {
	t.Parallel()
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
		if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &identity); err != nil || identity.Name != "Sion" {
			t.Errorf("wrong resumed persona: %+v, %v", identity, err)
		}
		if calls.Add(1)%2 == 1 {
			emitPersonaCall(w, "saved-agent-tool", "skill_index", map[string]any{})
			return
		}
		var output struct{ Type, Output string }
		if len(body.Input) == 0 || json.Unmarshal(body.Input[len(body.Input)-1], &output) != nil || output.Type != "function_call_output" || !strings.Contains(output.Output, "persona-change") {
			t.Error("selected agent did not execute the skill inventory tool")
		}
		emitResponse(w, "Saved conversation inventory checked.", true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "agents", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	op := "list"
	if _, err := client.Agent(h.ctx, protocol.AgentRequest{Operation: &op}); err != nil {
		t.Fatal(err)
	}
	frame, err := client.Receive(h.ctx)
	if err != nil || frame.Error != nil {
		t.Fatal("list failed", err, frame.Error)
	}
	result := frame.Payload.(protocol.AgentResult)
	agent := ""
	for _, row := range *result.Agents {
		if row.Name == "Sion" {
			agent = row.AgentID
		}
	}
	if agent == "" {
		t.Fatal("Sion starter missing")
	}
	if agent == client.Welcome.Agents[0].AgentID {
		t.Fatal("test needs a non-default saved selection")
	}
	client.Close()

	socket := h.socket
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 100, 28)
	tap.waitConversation(h)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/agents choose "+agent+"\r")
	for {
		frame := tap.next(t, h)
		if frame.Error != nil {
			t.Fatal(frame.Error.Code)
		}
		if result, ok := frame.Payload.(protocol.AgentResult); ok && result.Operation != nil && *result.Operation == "choose" {
			if result.AgentID == nil || *result.AgentID != agent {
				t.Fatal("choose changed identity")
			}
			break
		}
	}
	waitScreen(t, h, terminal, design.Text(design.English, "choose"))
	h.keys(terminal, "\x1b")
	tap.waitConversation(h)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "Saved conversation inventory request.\r")
	waitScreen(t, h, terminal, "Saved conversation inventory checked.")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)

	// Keep the same Core process and use a fresh TUI with no retained focus.
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 100, 28)
	for {
		frame := tap.next(t, h)
		if frame.Error != nil {
			t.Fatal(frame.Error.Code)
		}
		if history, ok := frame.Payload.(protocol.History); ok {
			if history.AgentID == nil || *history.AgentID != agent || history.ConversationID == nil || *history.ConversationID != "main:"+agent {
				t.Fatalf("reopened TUI lost saved selection: got %s, want agent %q", mustJSON(t, history), agent)
			}
			break
		}
	}
	tap.waitConversation(h)
	snapshot := waitScreen(t, h, terminal, "Saved conversation inventory checked.")
	rows := screenRows(snapshot)
	if !strings.Contains(rows[0], "Sion") || !strings.Contains(rows[len(rows)-2], "Sion") {
		t.Fatalf("saved conversation has wrong name: header=%q composer=%q", rows[0], rows[len(rows)-2])
	}
	if !strings.Contains(screenText(snapshot), "Saved conversation inventory request.") || calls.Load() != 2 {
		t.Fatal("reopen lost history or replayed the completed tool turn")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = socket
	h.stopCore(core)
}
