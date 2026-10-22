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

	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1SkillsInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("m1/skills.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		ID, Description, Body, Adopt, Read, Undo, Check string
		Tools                                           []string
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s\n", scenario.ID, scenario.Description, scenario.Body)
	patch := skills.Patch{ID: scenario.ID, Files: skills.Bundle{"SKILL.md": {Text: text}}}
	var calls atomic.Int32
	receipts := make(chan skills.Applied, 2)
	var adopted skills.Applied // Only the serial provider handler accesses this.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		names := map[string]bool{}
		for _, raw := range request.Tools {
			var declaration struct {
				Name, Type string
				Parameters json.RawMessage
			}
			if err := json.Unmarshal(raw, &declaration); err != nil {
				t.Error(err)
			}
			names[declaration.Name] = declaration.Type == "function" && json.Valid(declaration.Parameters)
		}
		for _, name := range scenario.Tools {
			if !names[name] {
				t.Errorf("request %d: skill tool %s not offered", n, name)
			}
		}
		if len(request.Input) == 0 {
			t.Error("no request context")
			emitResponse(w, "Missing context.", true)
			return
		}
		output := func(want string, value any) {
			var item struct{ Type, Output string }
			var result struct {
				Code   string
				Output json.RawMessage
			}
			if err := json.Unmarshal(request.Input[len(request.Input)-1], &item); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
				t.Error(err)
			}
			if item.Type != "function_call_output" || result.Code != want {
				t.Errorf("request %d tool result: %s", n, item.Output)
			}
			if value != nil {
				if err := json.Unmarshal(result.Output, value); err != nil {
					t.Error(err)
				}
			}
		}
		switch n {
		case 1:
			emitPersonaCall(w, "adopt", "write_skill", patch)
		case 2:
			output("ok", &adopted)
			if adopted.Version.ID != scenario.ID || adopted.Version.Revision != 1 || adopted.Notice == "" || adopted.Undo.Revision != 1 {
				t.Errorf("invalid adoption receipt: %+v", adopted)
			}
			receipts <- adopted
			emitResponse(w, "Saved.", true)
		case 3, 9:
			emitPersonaCall(w, fmt.Sprintf("index-%d", n), "skill_index", struct{}{})
		case 4, 10:
			var versions []skills.Version
			output("ok", &versions)
			var found *skills.Version
			for _, version := range versions {
				if version.ID == scenario.ID {
					found = &version
				}
			}
			if n == 4 {
				if found == nil || *found != adopted.Version || found.Description != scenario.Description {
					t.Error("adopted handle missing from index")
					emitResponse(w, "Missing skill.", true)
					return
				}
				emitPersonaCall(w, "read", "read_skill", *found)
			} else {
				if found != nil {
					t.Error("undo left skill in index after restart")
				}
				repeat := patch
				repeat.Expected = 2
				emitPersonaCall(w, "repeat", "write_skill", repeat)
			}
		case 5:
			var body skills.Body
			output("ok", &body)
			if body.Version != adopted.Version || body.Files["SKILL.md"].Text != text {
				t.Error("read_skill did not put the adopted body in next-turn context")
			}
			emitPersonaCall(w, "code", "write_skill", skills.Patch{ID: scenario.ID, Expected: 1, Files: skills.Bundle{"helper.sh": {Text: "echo synthetic", Executable: true}}})
		case 6, 11:
			var failure struct {
				Code     string
				Proposal *struct{ Type string }
			}
			output("failed", &failure)
			if n == 6 && (failure.Code != skills.ErrAcceptance.Error() || failure.Proposal == nil || failure.Proposal.Type != "summary") {
				t.Errorf("executable patch was not refused: %+v", failure)
			}
			if n == 11 && failure.Code != skills.ErrUndone.Error() {
				t.Errorf("undone content not suppressed: %+v", failure)
			}
			emitResponse(w, "Checked.", true)
		case 7:
			emitPersonaCall(w, "undo", "undo_skill", adopted.Undo)
		case 8:
			var undone skills.Applied
			output("ok", &undone)
			if undone.Version.Revision != 2 || undone.Notice == "" {
				t.Errorf("invalid undo receipt: %+v", undone)
			}
			receipts <- undone
			emitResponse(w, "Restored.", true)
		default:
			t.Errorf("unexpected provider request %d", n)
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	checkNotice := func(frames []protocol.Envelope, want string) {
		t.Helper()
		var notices []string
		for _, frame := range frames {
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem && item.Text != nil {
				notices = append(notices, *item.Text)
			}
		}
		if want == "" {
			if len(notices) != 0 {
				t.Fatalf("failed write emitted notices: %v", notices)
			}
			return
		}
		if len(notices) != 1 || notices[0] != want || strings.Contains(want, "\n") {
			t.Fatalf("committed notice: %v, want %q", notices, want)
		}
		waitScreen(t, h, terminal, want)
	}
	getReceipt := func() skills.Applied {
		t.Helper()
		select {
		case receipt := <-receipts:
			return receipt
		default:
			t.Fatal("no committed skill receipt")
			return skills.Applied{}
		}
	}
	frames := admittedTurn(t, h, c, conversation, "skill-adopt", scenario.Adopt)
	learned := getReceipt()
	checkNotice(frames, learned.Notice)
	checkNotice(admittedTurn(t, h, c, conversation, "skill-read", scenario.Read), "")
	frames = admittedTurn(t, h, c, conversation, "skill-undo", scenario.Undo)
	undone := getReceipt()
	checkNotice(frames, undone.Notice)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	terminal = h.startScreenTUI("TERM=xterm-256color")
	waitScreen(t, h, terminal, undone.Notice)
	checkNotice(admittedTurn(t, h, c, conversation, "skill-check", scenario.Check), "")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	c.Close()
	h.stopCore(core)
	if calls.Load() != 11 {
		t.Fatalf("provider calls=%d, want 11", calls.Load())
	}
}
