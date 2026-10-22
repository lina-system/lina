// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIAgentReconnectInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("m1/tui-agent-reconnect.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Agent, Input, Draft, Reply string
		Restarts                   []string
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	type request struct {
		Instructions string
		Input        []json.RawMessage
	}
	requests := make(chan request, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		emitResponse(w, fmt.Sprintf("%s-%d", scenario.Reply, calls.Add(1)), true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "agents", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	op := "create"
	if _, err = client.Agent(h.ctx, protocol.AgentRequest{Operation: &op, Name: &scenario.Agent}); err != nil {
		t.Fatal(err)
	}
	e, err := client.Receive(h.ctx)
	if err != nil || e.Error != nil {
		t.Fatal("agent setup failed", err)
	}
	created := e.Payload.(protocol.AgentResult)
	if created.AgentID == nil || *created.AgentID == client.Welcome.Agents[0].AgentID {
		t.Fatal("setup must create a non-default agent")
	}
	agent := *created.AgentID
	client.Close()
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 120, 35)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/agents choose "+agent+"\r")
	// The id also appears in the draft before the choose response. The agent
	// name is displayed only after that response has updated the selection.
	waitScreen(t, h, terminal, scenario.Agent)
	waitScreen(t, h, terminal, design.Text(design.English, "choose"))
	h.keys(terminal, "\x1b")
	if _, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return !strings.Contains(screenText(s), "/agents choose <id>")
	}); err != nil {
		t.Fatal("agent selection pager did not close", err)
	}
	waitComposerReady(t, h, terminal)
	assertRequest := func(input string) {
		t.Helper()
		select {
		case body := <-requests:
			var persona struct {
				AgentID string `json:"agent_id"`
			}
			if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &persona); err != nil {
				t.Fatal(err)
			}
			if persona.AgentID != agent {
				t.Fatalf("provider received agent %q, want selected agent %q", persona.AgentID, agent)
			}
			text := string(mustJSON(t, body.Input))
			if !strings.Contains(text, input) || !strings.Contains(text, scenario.Input) {
				t.Fatal("provider request lost the selected agent's input/history")
			}
		case <-h.ctx.Done():
			t.Fatal("provider request missing")
		}
	}
	h.keys(terminal, scenario.Input+"\r")
	waitScreen(t, h, terminal, scenario.Reply+"-1")
	assertRequest(scenario.Input)
	for index, restart := range scenario.Restarts {
		draft := fmt.Sprintf("%s-%d", scenario.Draft, index)
		h.keys(terminal, draft)
		waitScreen(t, h, terminal, draft)
		switch restart {
		case "graceful":
			h.stopCore(core)
		case "forced":
			core.killAndWait()
		default:
			t.Fatalf("unknown restart %q", restart)
		}
		waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
		core = start(true)
		waitScreen(t, h, terminal, design.Text(design.English, "live"))
		// An edit orders the assertion after the reconnect redraw, without sleeps.
		h.keys(terminal, "x")
		snapshot := waitScreen(t, h, terminal, draft+"x")
		if !strings.Contains(screenText(snapshot), scenario.Agent) || calls.Load() != int32(index+1) {
			t.Error("reconnect lost the chosen agent or automatically sent the draft")
		}
		h.keys(terminal, "\r")
		waitScreen(t, h, terminal, fmt.Sprintf("%s-%d", scenario.Reply, index+2))
		assertRequest(draft + "x")
	}
	// Remove the selected agent through another client before the next welcome.
	// The retained draft must not become input for the remaining default agent.
	missingDraft := scenario.Draft + "-MISSING"
	h.keys(terminal, missingDraft)
	waitScreen(t, h, terminal, missingDraft)
	client, err = clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "agents", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	op = "delete"
	if _, err = client.Agent(h.ctx, protocol.AgentRequest{Operation: &op, AgentID: &agent}); err != nil {
		t.Fatal(err)
	}
	e, err = client.Receive(h.ctx)
	if err != nil || e.Error != nil {
		t.Fatal("agent deletion failed", err)
	}
	client.Close()
	h.stopCore(core)
	waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
	core = start(true)
	waitScreen(t, h, terminal, design.Text(design.English, "agents.choose_required"))
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	h.keys(terminal, "\rx")
	waitScreen(t, h, terminal, missingDraft+"x")
	if calls.Load() != int32(len(scenario.Restarts)+1) {
		t.Fatal("missing agent's draft reached the provider")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}
