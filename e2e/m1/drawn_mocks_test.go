// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/results/mocks"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestM3DrawnMocks(t *testing.T) {
	var mu sync.Mutex
	var current *result.ResultRef
	foreground, plans, draws := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var input mocks.ModelInput
		for _, raw := range request.Input {
			var item struct {
				Role    string
				Content json.RawMessage
			}
			if json.Unmarshal(raw, &item) != nil || item.Role != "user" {
				continue
			}
			var text string
			if json.Unmarshal(item.Content, &text) == nil {
				_ = json.Unmarshal([]byte(text), &input)
			} else {
				var parts []struct{ Text string }
				if json.Unmarshal(item.Content, &parts) == nil {
					for _, p := range parts {
						_ = json.Unmarshal([]byte(p.Text), &input)
						if input.Phase != "" {
							break
						}
					}
				}
			}
			if input.Phase != "" {
				break
			}
		}
		t.Logf("provider phase=%q items=%d", input.Phase, len(request.Input))
		mu.Lock()
		defer mu.Unlock()
		switch input.Phase {
		case "checklist":
			plans++
			if plans == 2 && (!strings.Contains(input.Request, "Feedback:") || input.Previous == "") {
				t.Error("feedback did not include current revision")
			}
			emitResponse(w, `{"checklist":[{"id":"tasks","description":"Open tasks","action":"none","target":""},{"id":"filter","description":"Today","action":"toggle","target":"list"}]}`, true)
		case "draw":
			draws++
			class := "stack"
			if draws == 2 {
				class = "stack compact"
			}
			emitResponse(w, string(mustJSON(t, map[string]string{"fragment": `<main class="` + class + `"><h1 data-check="tasks">Open tasks</h1><button data-check="filter" data-action="toggle" data-target="list">Today</button><section class="card" id="list">One task</section></main>`})), true)
		default:
			foreground++
			if foreground%2 == 1 {
				brief := runs.Brief{Goal: "Show open tasks with a Today control", Scope: "Static mock", NotTouch: "No network or Core data", Done: "All mock checks pass", Checks: []runs.Check{{Name: "mock checks", Kind: runs.Claimed}}, Result: "mock", Allow: []string{}, Mock: current}
				if current != nil {
					brief.Goal = "Make the spacing smaller"
				}
				emitPersonaCall(w, "draw-mock", "start_run", brief)
			} else {
				emitResponse(w, "The mock is being drawn.", true)
			}
		}
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	browser := ""
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			browser = path
			break
		}
	}
	if browser == "" {
		t.Fatal("Chromium is required for drawn mock acceptance")
	}
	bin := t.TempDir()
	if err := os.Symlink(browser, filepath.Join(bin, "google-chrome")); err != nil {
		t.Fatal(err)
	}
	start := func(_ bool) *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "PATH="+bin, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connect(p).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "mocks", Version: "1.0"})
	if c.Welcome.ProtocolVersion != "1.1" {
		t.Fatal("mock client did not negotiate 1.1")
	}
	waitMock := func(frames []protocol.Envelope) protocol.Result {
		t.Helper()
		for {
			for _, e := range frames {
				if e.Kind == protocol.KindBackgroundNotice || e.Kind == protocol.KindError {
					raw, _ := json.Marshal(e.Payload)
					t.Logf("run notice: %s", raw)
				}
				if p, ok := e.Payload.(protocol.Result); ok && p.Type == protocol.ResultTypeMock {
					return p
				}
			}
			e, err := c.Receive(h.ctx)
			if err != nil {
				mu.Lock()
				t.Logf("counts foreground=%d plans=%d draws=%d", foreground, plans, draws)
				mu.Unlock()
				t.Fatal(err)
			}
			frames = []protocol.Envelope{e}
		}
	}
	first := waitMock(admittedTurn(t, h, c, conversation, "mock-first", "Show an open tasks mock"))
	if first.Body == nil {
		t.Fatal("no typed mock body")
	}
	var firstBody result.Mock
	if err := result.Decode(*first.Body, &firstBody); err != nil {
		t.Fatal(err)
	}
	if len(firstBody.Checks) != 7 || firstBody.Previous != nil {
		t.Fatal("invalid first evaluation")
	}
	mu.Lock()
	current = &result.ResultRef{ID: first.ResultID, Revision: 1}
	mu.Unlock()
	second := waitMock(admittedTurn(t, h, c, conversation, "mock-feedback", "Make the spacing smaller"))
	if second.ResultID != first.ResultID || second.Revision != "2" {
		t.Fatalf("feedback lost result identity: %+v", second)
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h, protocol.Capability{Name: "mocks", Version: "1.0"})
	defer c.Close()
	rpc := func(p protocol.MockRequest) protocol.MockResponse {
		t.Helper()
		id, err := c.Mock(h.ctx, p, "")
		if err != nil {
			t.Fatal(err)
		}
		for {
			e, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if e.ReplyTo == nil || *e.ReplyTo != id {
				continue
			}
			response, ok := e.Payload.(protocol.MockResponse)
			if !ok {
				t.Fatalf("mock request refused: kind=%s error=%+v", e.Kind, e.Error)
			}
			return response
		}
	}
	for _, rev := range []json.Number{"1", "2"} {
		got := rpc(protocol.MockRequest{Action: "read", ResultID: first.ResultID, Revision: rev, ConversationID: conversation})
		if got.Result == nil || got.Result.Revision != rev || got.Result.Body == nil {
			t.Fatal("retained revision unavailable after restart")
		}
	}
	for _, kind := range []string{"page", "png"} {
		p := protocol.MockRequest{Action: "asset", ResultID: first.ResultID, Revision: "2", ConversationID: conversation, Asset: &kind}
		var data []byte
		for {
			answer := rpc(p)
			if answer.Data == nil {
				t.Fatal("no asset bytes")
			}
			chunk, err := base64.StdEncoding.DecodeString(*answer.Data)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, chunk...)
			p.Offset = answer.Next
			if answer.Next == nil {
				break
			}
		}
		if kind == "page" {
			source, err := mocks.Source(data)
			if err != nil || !strings.Contains(source, "compact") {
				t.Fatal("export not current page")
			}
		} else if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatal("PNG asset missing")
		}
	}
	exported := filepath.Join(h.root, "mock.png")
	command := h.command("mock-export", "--socket", h.socket, "--conversation", conversation, "--result", first.ResultID, "--revision", "2", "--asset", "png", "--output", exported)
	command.start(t)
	command.wait(t, 0)
	png, err := os.ReadFile(exported)
	if err != nil || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("person-facing export did not write PNG")
	}
	duplicate := h.command("mock-export", "--socket", h.socket, "--conversation", conversation, "--result", first.ResultID, "--revision", "2", "--asset", "png", "--output", exported)
	duplicate.start(t)
	duplicate.wait(t, 70)
	retained, err := os.ReadFile(exported)
	if err != nil || string(retained) != string(png) {
		t.Fatal("export replaced an existing file")
	}
	// A legacy client sees the retained mock checklist as ordinary text.
	legacy, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err = legacy.History(h.ctx, protocol.HistoryGet{ConversationID: &conversation}); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := legacy.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind == protocol.KindHistory {
			break
		}
	}
	topic := "conversation:" + conversation
	if _, err = legacy.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	found := false
	for {
		e, err := legacy.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := e.Payload.(protocol.Snapshot)
		if !ok {
			continue
		}
		for _, item := range snapshot.Items {
			if item.Kind == protocol.KindItemDone && strings.Contains(string(item.Payload), "Export PNG:") {
				found = true
			}
		}
		break
	}
	if !found {
		t.Fatal("legacy checklist/export fallback missing")
	}
	mu.Lock()
	defer mu.Unlock()
	if plans != 2 || draws != 2 {
		t.Fatalf("restart repeated model work: plans=%d draws=%d", plans, draws)
	}
}
