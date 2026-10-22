// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUISpeakerBoundariesInstalledCore(t *testing.T) {
	for _, language := range []string{"", "go"} {
		t.Run("language="+language, func(t *testing.T) {
			testTUISpeakerBoundariesInstalledCore(t, language)
		})
	}
}

func testTUISpeakerBoundariesInstalledCore(t *testing.T, language string) {
	t.Helper()
	const input = "Show the code result"
	const code = "var 한글 = 42"
	reply := "```" + language + "\n\n" + code + "\n\n```"
	var calls atomic.Int32
	requests := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 1 {
			t.Error("unexpected additional provider request")
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		var body struct {
			Instructions string
			Input        []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), input) {
			t.Error("provider request lost typed input")
		}
		var persona struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &persona); err != nil {
			t.Error(err)
			return
		}
		requests <- persona.AgentID
		emitResponse(w, reply, true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	socket := h.socket
	tap := newAccountTap(t, h, socket, true)
	h.socket = tap.socket
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "COLORTERM=truecolor", "LINA_GLYPHS=ascii", "LC_ALL=C"}, false, true, 100, 34)
	h.socket = socket
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
			// The tap observes the reply before the TUI renders it. The same ID
			// is already in the typed command (and the previous create result),
			// so wait for this operation's pager before sending Escape.
			if _, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
				rows := screenRows(s)
				return strings.Contains(screenText(s), design.Text(design.English, operation)+" "+*result.AgentID) &&
					strings.Contains(rows[len(rows)-1], "Esc "+design.Text(design.English, "shortcuts.close"))
			}); err != nil {
				t.Fatalf("agent %s result not rendered: %v", operation, err)
			}
			h.keys(terminal, "\x1b")
			waitComposerReady(t, h, terminal)
			return *result.AgentID
		}
	}
	agent := agentCommand("/agents create "+strings.Repeat("A", 37), "create")
	agentCommand("/agents choose "+agent, "choose")
	tap.waitConversation(h)
	h.resizeTerminal(terminal, 40, 34)
	if _, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		return len(s.Cells) == 34 && len(s.Cells[0]) == 40 && !s.CursorHidden &&
			strings.Contains(screenRows(s)[33], design.Text(design.English, "live")) &&
			strings.Contains(screenRows(s)[33], "? "+design.Text(design.English, "shortcuts.title"))
	}); err != nil {
		t.Fatal("narrow composer not ready", err)
	}
	h.keys(terminal, input+"\r")
	for {
		frame := tap.next(t, h)
		if frame.Error != nil {
			t.Fatalf("Core refused %s: %+v", tap.requestKind(frame), frame.Error)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	select {
	case got := <-requests:
		if got != agent {
			t.Fatalf("provider routed to %q instead of selected agent %q", got, agent)
		}
	case <-h.ctx.Done():
		t.Fatal("missing provider request")
	}
	// The tap sees Core's event before the TUI consumes it. Observe the
	// rendered completion before using draft edits as redraw barriers.
	worked := strings.TrimSuffix(design.Text(design.English, "activity.worked_for"), "%s")
	waitScreen(t, h, terminal, worked)
	h.keys(terminal, "capture-one")
	first := waitScreen(t, h, terminal, "capture-one")
	rows := screenRows(first)
	t.Logf("40x34 parsed cells:\n%s", strings.Join(rows, "\n"))
	codeRow, workedRow := -1, -1
	for i, row := range rows {
		if strings.Contains(row, code) {
			codeRow = i
		}
		if strings.HasPrefix(row, worked) {
			if workedRow >= 0 {
				t.Fatal("duplicate completion metadata")
			}
			workedRow = i
		}
	}
	if codeRow < 2 || workedRow != codeRow+2 {
		t.Fatalf("wide code or empty boundary row lost: code=%d worked=%d; cells=%q", codeRow, workedRow, rows)
	}
	prefix := rows[codeRow-1]
	if !strings.HasPrefix(prefix, "* ") || !strings.HasSuffix(prefix, ":") || (render.Unicode{}).Width(prefix)+1 > 20 {
		t.Fatalf("speaker gutter did not reserve usable width: %q", prefix)
	}
	if rows[codeRow+1] != "" || rows[codeRow-2] != "" || strings.TrimSpace(rows[codeRow]) != code {
		t.Fatalf("literal code boundaries or turn separator changed: %q", rows)
	}
	h.keys(terminal, "-two")
	second := waitScreen(t, h, terminal, "capture-one-two")
	if !reflect.DeepEqual(rows[:workedRow+1], screenRows(second)[:workedRow+1]) {
		t.Fatal("unchanged conversation changed across composer redraw")
	}
	if calls.Load() != 1 {
		t.Fatal("missing or replayed model exchange")
	}
	h.keys(terminal, "\x03\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}
