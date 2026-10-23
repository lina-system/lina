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
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUISavedAgentReopenInstalledCore(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name   string
		create bool
	}{
		{"created-renamed", true},
		{"starter", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			savedAgentReopen(t, scenario.create)
		})
	}
}

func savedAgentReopen(t *testing.T, create bool) {
	t.Helper()
	name := "Sion"
	if create {
		name = "QA Atlas"
	}
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
	if !create && agent == "" {
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
	agentCommand := func(command, operation string) string {
		t.Helper()
		h.keys(terminal, command+"\r")
		for {
			frame := tap.next(t, h)
			if frame.Error != nil {
				t.Fatal(frame.Error.Code)
			}
			result, ok := frame.Payload.(protocol.AgentResult)
			if !ok {
				continue
			}
			if result.Operation == nil || *result.Operation != operation || result.AgentID == nil {
				t.Fatalf("unexpected agent response: %+v", result)
			}
			if _, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
				text := screenText(s)
				return strings.Contains(text, design.Text(design.English, operation)) && strings.Contains(text, *result.AgentID)
			}); err != nil {
				t.Fatal("agent result not rendered", err)
			}
			h.keys(terminal, "\x1b")
			waitComposerReady(t, h, terminal)
			return *result.AgentID
		}
	}
	if create {
		agent = agentCommand("/agents create "+name, "create")
	}
	if chosen := agentCommand("/agents choose "+agent, "choose"); chosen != agent {
		t.Fatal("choose changed identity")
	}
	tap.waitConversation(h)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "Saved conversation inventory request.\r")
	waitScreen(t, h, terminal, "Saved conversation inventory checked.")
	displayedName := name
	if create {
		displayedName = "QA Nova"
		if renamed := agentCommand("/agents rename "+agent+" "+displayedName, "rename"); renamed != agent {
			t.Fatal("rename changed identity")
		}
	}
	assertName := func(stage string, snapshot tuiterm.Snapshot) {
		t.Helper()
		rows := screenRows(snapshot)
		t.Logf("%s: header=%q composer=%q", stage, rows[0], rows[len(rows)-2])
		if !strings.Contains(rows[0], displayedName) || !strings.Contains(rows[len(rows)-2], displayedName) {
			t.Fatalf("%s: wrong name: header=%q composer=%q; want %q", stage, rows[0], rows[len(rows)-2], displayedName)
		}
	}
	assertName("before quit", waitComposerReady(t, h, terminal))
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)

	select {
	case <-core.done:
		t.Fatal("quitting the TUI stopped Core")
	default:
	}

	// Keep the same Core process and use a fresh TUI with no retained focus.
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 100, 28)
	var restored protocol.History
	for {
		frame := tap.next(t, h)
		if frame.Error != nil {
			t.Fatal(frame.Error.Code)
		}
		if history, ok := frame.Payload.(protocol.History); ok {
			restored = history
			break
		}
	}
	tap.waitConversation(h)
	rows := screenRows(waitComposerReady(t, h, terminal))
	t.Logf("reopened: header=%q composer=%q", rows[0], rows[len(rows)-2])
	if restored.AgentID == nil || *restored.AgentID != agent || restored.ConversationID == nil || *restored.ConversationID != "main:"+agent {
		t.Fatalf("reopened TUI lost saved selection: got %s, want agent %q", mustJSON(t, restored), agent)
	}
	snapshot := waitScreen(t, h, terminal, "Saved conversation inventory checked.")
	assertName("reopened", snapshot)
	if !strings.Contains(screenText(snapshot), "Saved conversation inventory request.") || calls.Load() != 2 {
		t.Fatal("reopen lost history or replayed the completed tool turn")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = socket
	h.stopCore(core)
}
