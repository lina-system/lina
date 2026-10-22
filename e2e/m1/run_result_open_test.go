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

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1RunResultOpenInstalledCore(t *testing.T) {
	t.Parallel()

	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) { runResultOpenLocalized(t, language) })
	}
}

func runResultOpenLocalized(t *testing.T, language design.Language) {
	var scenario struct{ Input, Artifact, Prior string }
	data, err := os.ReadFile("run-result-open.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var foreground, background atomic.Int32
	var prior atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Instructions string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			background.Add(1)
			emitResponse(w, string(mustJSON(t, runs.Artifact{Body: scenario.Artifact, Done: true})), true)
			return
		}
		if !prior.Swap(true) {
			emitResponse(w, strings.Repeat("EARLIER-REPLY ", 120), true)
			return
		}
		n := foreground.Add(1)
		if n > 10 {
			t.Error("foreground exceeded hard budget")
			w.WriteHeader(500)
			return
		}
		emitPersonaCall(w, fmt.Sprintf("read-%d", n), "skill_index", map[string]any{})
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	client, conversation := conversationClient(t, h)
	defer client.Close()
	admittedTurn(t, h, client, conversation, "prior-result", scenario.Prior)
	admittedTurn(t, h, client, conversation, "result-open", scenario.Input)
	for {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Kind == protocol.KindBackgroundNotice {
			break
		}
	}
	if foreground.Load() != 10 || background.Load() != 1 {
		t.Fatalf("requests foreground=%d background=%d", foreground.Load(), background.Load())
	}
	client.Close()
	h.stopCore(core)
	core = start(true)
	locale := "en_US.UTF-8"
	if language == design.Korean {
		locale = "ko_KR.UTF-8"
	}
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=" + locale}, false, true, 100, 32)
	strip := waitScreen(t, h, terminal, design.Text(language, "running")+" 1")
	assertLocalizedRun := func(surface, text string) {
		t.Helper()
		if strings.Contains(text, "ready_for_review:") {
			t.Fatalf("%s exposes the internal run state: %s", surface, text)
		}
		label, ok := design.Lookup(language, "running.state.ready_for_review")
		if !ok || !strings.Contains(text, label+": "+scenario.Input) {
			t.Fatalf("%s lacks the localized state and original summary: %s", surface, text)
		}
	}
	assertLocalizedRun("running strip", screenText(strip))
	h.keys(terminal, "\x12") // Running: the owner opens a landed row.
	list := waitScreen(t, h, terminal, design.Text(language, "running.keys"))
	assertLocalizedRun("running list", screenText(list))
	h.keys(terminal, "\r")
	waitScreen(t, h, terminal, scenario.Artifact)
	h.keys(terminal, "\x1b")
	waitScreen(t, h, terminal, design.Text(language, "live"))
	h.keys(terminal, "\x0f") // The notice's card remains readable after opening removes its row.
	waitScreen(t, h, terminal, scenario.Artifact)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	if foreground.Load() != 10 || background.Load() != 1 {
		t.Fatal("restart replayed provider work")
	}
}
