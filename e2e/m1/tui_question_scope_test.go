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
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Exercise the installed Core's ask_question tool and answer continuation, then
// check both a live switch and a fresh TUI's replay of the terminal card.
func TestM1TUIQuestionConversationScopeInstalledCore(t *testing.T) {
	t.Parallel()
	const question = "Which synthetic capsule color?"
	const recommendation = "Blue keeps the fixture simple."
	const otherReply = "Orbit fixture reply is isolated."
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "capsule", "ask_question", map[string]any{"question": question, "recommendation": recommendation, "default": "Blue", "choices": []string{"Blue", "Orange"}})
		case 2:
			var q waiting.ConversationQuestion
			if err := json.Unmarshal(assertAskResult(t, request.Input, "capsule", "ok", ""), &q); err != nil || q.Conversation == "" {
				t.Error("Core did not save the question's conversation", err)
			}
			emitResponse(w, "Capsule question is ready.", true)
		case 3:
			if body := string(mustJSON(t, request.Input)); !strings.Contains(body, "Blue") || !strings.Contains(body, question) {
				t.Error("answer continuation lost the question or answer")
			}
			emitResponse(w, "Blue capsule selected.", true)
		default:
			body := string(mustJSON(t, request.Input))
			if strings.Contains(body, question) || strings.Contains(body, recommendation) {
				t.Error("foreign question entered Orbit provider context")
			}
			emitResponse(w, otherReply, true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer h.stopCore(core)
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "question-scope", []protocol.Capability{{Name: "agents", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	original := c.Welcome.Agents[0].AgentID
	op, name := "create", "Orbit-QA"
	if _, err := c.Agent(h.ctx, protocol.AgentRequest{Operation: &op, Name: &name}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	created, ok := frame.Payload.(protocol.AgentResult)
	if err != nil || frame.Error != nil || !ok || created.AgentID == nil {
		t.Fatalf("create: %+v %v", frame, err)
	}
	other := *created.AgentID
	c.Close()

	socket := h.socket
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	defer func() { h.socket = socket }()
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 120, 35)
	tap.waitConversation(h)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "Prepare a capsule.\r")
	waitScreen(t, h, terminal, "Capsule question is ready.")
	h.keys(terminal, "\x1b[?3u\x1b[17~")
	waitScreen(t, h, terminal, "Blue / Orange")
	h.keys(terminal, "Blue\x1b[13;1:1u")
	for {
		frame := tap.next(t, h)
		if answer, ok := frame.Payload.(protocol.QuestionAnswer); ok {
			if frame.Error != nil || answer.Outcome == nil || *answer.Outcome != protocol.DecisionOutcomeResolved {
				t.Fatalf("answer: %+v", frame)
			}
			break
		}
	}
	waitScreen(t, h, terminal, design.Text(design.English, "resolved"))
	h.keys(terminal, "\x1b")
	waitScreen(t, h, terminal, "Blue capsule selected.")
	waitComposerReady(t, h, terminal)

	choose := func(id string) {
		t.Helper()
		h.keys(terminal, "/agents choose "+id+"\r")
		for {
			frame := tap.next(t, h)
			if result, ok := frame.Payload.(protocol.AgentResult); ok && result.Operation != nil && *result.Operation == "choose" {
				if frame.Error != nil || result.AgentID == nil || *result.AgentID != id {
					t.Fatalf("choose: %+v", frame)
				}
				break
			}
		}
		waitScreen(t, h, terminal, design.Text(design.English, "choose"))
		tap.waitConversation(h)
		h.keys(terminal, "\x1b")
		waitComposerReady(t, h, terminal)
	}
	assertAbsent := func(stage string) {
		t.Helper()
		text := screenText(waitComposerReady(t, h, terminal))
		// The global pending list can still be refreshing. Check the terminal
		// question card, not a legitimate cross-agent Waiting for me row.
		cardTitle := "[" + design.Text(design.English, string(protocol.KindQuestion)) + "] " + question
		if strings.Contains(text, cardTitle) || strings.Contains(text, recommendation) {
			t.Errorf("%s: foreign resolved question in Orbit conversation:\n%s", stage, text)
		}
	}
	choose(other)
	assertAbsent("live switch")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)

	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 120, 35)
	tap.waitConversation(h)
	waitComposerReady(t, h, terminal)
	// Returning to the origin proves the replayed card arrived in this client;
	// the next switch must filter it, not merely win a race with the snapshot.
	choose(original)
	waitScreen(t, h, terminal, question)
	waitScreen(t, h, terminal, design.Text(design.English, "resolved"))
	choose(other)
	assertAbsent("fresh client replay")
	h.keys(terminal, "An ordinary Orbit message.\r")
	waitScreen(t, h, terminal, otherReply)
	assertAbsent("ordinary reply")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
}
