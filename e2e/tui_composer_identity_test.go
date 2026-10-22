// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2TUIComposerIdentityInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("m2/tui-composer-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Agent, Input, Reply string
		Width, Height       int
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	type request struct {
		Instructions string
		Input        []json.RawMessage
	}
	requests := make(chan request, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		emitResponse(w, scenario.Reply, true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	terminal, tap := h.startConversationTUI(scenario.Width, scenario.Height, "TERM=xterm-256color", "LC_ALL=C")
	waitComposerReady(t, h, terminal)
	agentCommand := func(command, operation string) protocol.AgentResult {
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
			return result
		}
	}
	created := agentCommand("/agents create "+scenario.Agent, "create")
	agent := *created.AgentID
	agentCommand("/agents choose "+agent, "choose")
	tap.waitConversation(h)
	assertComposer := func(stage string, snapshot tuiterm.Snapshot) {
		t.Helper()
		rows := screenRows(snapshot)
		composer := rows[scenario.Height-2]
		t.Logf("%s: header=%q composer=%q", stage, rows[0], composer)
		if !strings.Contains(rows[0], scenario.Agent) || !strings.Contains(composer, scenario.Agent) || strings.Contains(composer, "LINA") {
			t.Errorf("%s: header and empty composer must identify %q", stage, scenario.Agent)
		}
	}
	assertComposer("chosen", waitComposerReady(t, h, terminal))
	h.keys(terminal, scenario.Input+"\r")
	snapshot := waitScreen(t, h, terminal, scenario.Reply)
	assertComposer("replied", snapshot)
	if strings.Count(screenText(snapshot), scenario.Agent) < 3 {
		t.Error("selected agent missing from header, reply speaker or composer")
	}
	select {
	case body := <-requests:
		var persona struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &persona); err != nil || persona.AgentID != agent {
			t.Fatal("provider did not receive the chosen agent", err)
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), scenario.Input) {
			t.Fatal("provider did not receive the synthetic input")
		}
	case <-h.ctx.Done():
		t.Fatal("provider request missing")
	}
	h.stopCore(core)
	waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
	core = start(true)
	waitComposerReady(t, h, terminal)
	// An input edit and clear order this observation after the reconnect redraw.
	h.keys(terminal, "x")
	if _, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return screenRows(s)[scenario.Height-2] == "› x"
	}); err != nil {
		t.Fatal("reconnected draft not rendered", err)
	}
	h.keys(terminal, "\x7f")
	snapshot, err = terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return strings.Contains(screenRows(s)[scenario.Height-2], "Alt+Enter")
	})
	if err != nil {
		t.Fatal("empty composer not restored", err)
	}
	assertComposer("reconnected", snapshot)
	if !strings.Contains(screenText(snapshot), scenario.Reply) {
		t.Error("selected agent history missing after reconnect")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}
