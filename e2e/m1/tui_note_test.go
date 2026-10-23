// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM2TUINoteNextRoundInstalledCore runs m2/tui-note.json through installed
// Core and a real PTY TUI. It holds a background provider response after ten
// foreground tool rounds, then checks one scoped follow-up in the next request.
// External model wires are synthetic.
//
// Reproduce the real-use journey: ten reads hand off to a background run,
// whose current provider response is held while the person submits /note.
func TestM2TUINoteNextRoundInstalledCore(t *testing.T) {
	var fixture struct{ Input, Note, Artifact string }
	raw, err := os.ReadFile("../m2/tui-note.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var foreground, background atomic.Int32
	started, release, observed := make(chan struct{}), make(chan struct{}), make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Instructions string
			Input        json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			if background.Add(1) == 1 {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				emitResponse(w, string(mustJSON(t, runs.Artifact{Body: "First pass", Done: true})), true)
			} else {
				observed <- string(request.Input)
				emitResponse(w, string(mustJSON(t, runs.Artifact{Body: fixture.Artifact, Done: true})), true)
			}
			return
		}
		n := foreground.Add(1)
		if n > 10 {
			t.Error("command reached the foreground provider")
			emitResponse(w, "Unexpected foreground", true)
			return
		}
		emitPersonaCall(w, fmt.Sprintf("read-%d", n), "skill_index", map[string]any{})
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { unblock(); h.stopCore(core) }()
	socket := h.socket
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color", "LC_ALL=C")
	h.socket = socket
	waitComposerReady(t, h, terminal)
	h.keys(terminal, fixture.Input+"\r")
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal("background request did not start")
	}
	waitScreen(t, h, terminal, design.Text(design.English, "running")+" 1")
	h.keys(terminal, "/note "+fixture.Note+"\r")
	for {
		e := tap.next(t, h)
		kind := tap.requestKind(e)
		if kind == protocol.KindInput {
			if foreground.Load() > 10 {
				t.Fatal("/note became conversation input")
			}
			continue
		}
		if kind != protocol.KindFollowUp {
			continue
		}
		if e.Error != nil {
			t.Fatalf("/note did not target the running background run: %s", e.Error.Code)
		}
		p, ok := e.Payload.(protocol.FollowUp)
		if !ok || p.Text != fixture.Note || p.RunID == "" {
			t.Fatalf("follow-up changed: %+v", e.Payload)
		}
		break
	}
	waitScreen(t, h, terminal, design.Text(design.English, "control.note"))
	unblock()
	select {
	case input := <-observed:
		if strings.Count(input, fixture.Note) != 1 || strings.Contains(input, "/note ") {
			t.Fatalf("next background round lost or changed note: %s", input)
		}
	case <-h.ctx.Done():
		t.Fatal("next background round did not receive note")
	}
	h.keys(terminal, "\x1b")
	waitScreen(t, h, terminal, design.Text(design.English, "running.state.ready_for_review"))
	h.keys(terminal, "\x12\r")
	waitScreen(t, h, terminal, fixture.Artifact)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	if foreground.Load() != 10 || background.Load() != 2 {
		t.Fatalf("provider calls foreground=%d background=%d", foreground.Load(), background.Load())
	}
}
