// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2TUIBoardInstalledCore(t *testing.T) {
	var fixture struct{ Input, Artifact string }
	raw, err := os.ReadFile("../m2/tui-board.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Instructions string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			emitResponse(w, string(mustJSON(t, runs.Artifact{Body: fixture.Artifact, Done: true})), true)
			return
		}
		n := calls.Add(1)
		if n > 10 {
			t.Error("board command reached provider")
			http.Error(w, "unexpected", 500)
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
	tap := newAccountTap(t, h, h.socket)
	h.socket = tap.socket
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 100, 36)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, fixture.Input+"\r")
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal("background did not start")
	}
	waitScreen(t, h, terminal, design.Text(design.English, "running")+" 1")
	outside := filepath.Join(h.root, "versions", "records", "board-outside.md")
	if err = os.WriteFile(outside, []byte("Synthetic outside edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h.keys(terminal, "/board\r")
	waiting := ""
	for waiting == "" {
		e := tap.next(t, h)
		if tap.requestKind(e) != protocol.KindWaitingGet {
			continue
		}
		if e.Error != nil {
			t.Fatal(e.Error.Code)
		}
		p, ok := e.Payload.(protocol.Waiting)
		if !ok || p.Rows == nil {
			t.Fatalf("waiting response: %+v", e)
		}
		for _, row := range *p.Rows {
			if row.Ref.Source == "outside" {
				waiting = row.Line.Sentence
			}
		}
	}
	view := screenText(waitScreen(t, h, terminal, waiting))
	last := -1
	for _, part := range []string{design.Text(design.English, "waiting"), waiting, design.Text(design.English, "running"), fixture.Input, design.Text(design.English, "board.done")} {
		pos := strings.Index(view, part)
		if pos <= last {
			t.Fatalf("board group/order missing %q: %s", part, view)
		}
		last = pos
	}
	if err = os.Remove(outside); err != nil {
		t.Fatal(err)
	}
	unblock()
	_, err = terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		v := screenText(s)
		done := strings.Index(v, design.Text(design.English, "board.done"))
		result := strings.Index(v, design.Text(design.English, "running.state.ready_for_review"))
		return !strings.Contains(v, waiting) && done >= 0 && result > done
	})
	if err != nil {
		t.Fatalf("open board did not update its groups: %v; %s", err, screenText(terminal.screen.Snapshot()))
	}
	h.keys(terminal, "\r")
	waitScreen(t, h, terminal, design.Text(design.English, "board.open"))
	h.keys(terminal, "o")
	waitScreen(t, h, terminal, fixture.Artifact)
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	// Unknown commands are local even while conversation is available.
	h.keys(terminal, "/not-a-command synthetic\r")
	waitScreen(t, h, terminal, design.Text(design.English, "command.unknown"))
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	if calls.Load() != 10 {
		t.Fatalf("foreground calls: %d", calls.Load())
	}
}
