// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

// This synthetic installed worker is a separate executable over the same stdio
// boundary as Codex. It changes only its private work directory and verifies the
// full launch/request surface, including omission of settings and credentials.
func codexSessionFixture() {
	if os.Args[1] == "--version" {
		fmt.Println("codex-cli 99.0.0")
		return
	}
	if len(os.Args) != 4 || strings.Join(os.Args[1:], " ") != "app-server --listen stdio://" {
		os.Exit(91)
	}
	if os.Getenv("LINA_RESPONSES_API_KEY") != "" || os.Getenv("CODEX_HOME") != "/fixture/codex-home" {
		os.Exit(92)
	}
	scan := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(93)
		}
	}
	initialized := false
	cwd, turn, state := "", "", "idle"
	switchFailures := false
	count := 0
	for scan.Scan() {
		var r struct {
			ID     json.RawMessage
			Method string
			Params map[string]json.RawMessage
		}
		if json.Unmarshal(scan.Bytes(), &r) != nil {
			os.Exit(94)
		}
		reply := func(v any) { send(map[string]any{"id": r.ID, "result": v}) }
		notify := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
		switch r.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "fixture/99.0.0"})
		case "initialized":
			initialized = true
		case "thread/start":
			if !initialized || r.Params["cwd"] == nil {
				os.Exit(95)
			}
			for key := range r.Params {
				if key != "cwd" && key != "dynamicTools" {
					os.Exit(95)
				}
			}
			json.Unmarshal(r.Params["cwd"], &cwd)
			reply(map[string]any{"thread": map[string]any{"id": "fixture-thread"}, "cwd": cwd, "model": "worker-own-model"})
		case "turn/start":
			if len(r.Params) != 2 {
				os.Exit(96)
			}
			count++
			if count == 1 {
				var inputs []struct{ Text string }
				_ = json.Unmarshal(r.Params["input"], &inputs)
				switchFailures = len(inputs) == 1 && inputs[0].Text == "switch refusal and terminal connection failure"
			}
			turn = fmt.Sprintf("turn-%d", count)
			state = "active"
			notify("turn/started", map[string]any{"threadId": "fixture-thread", "turn": map[string]any{"id": turn, "status": "inProgress"}})
			if count == 2 {
				if err := os.WriteFile(filepath.Join(cwd, "result.txt"), []byte("LINA_WORKER_RESULT"), 0600); err != nil {
					os.Exit(97)
				}
				notify("item/completed", map[string]any{"threadId": "fixture-thread", "turnId": turn, "item": map[string]any{"type": "agentMessage", "id": "result", "text": "LINA_WORKER_RESULT"}})
				state = "idle"
				completed := map[string]any{"id": turn, "status": "completed"}
				if switchFailures {
					completed["status"] = "failed"
					completed["error"] = map[string]any{"message": "fixture provider disconnected after output", "codexErrorInfo": map[string]any{"responseStreamDisconnected": map[string]any{}}}
				}
				notify("turn/completed", map[string]any{"threadId": "fixture-thread", "turn": completed})
			}
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
		case "turn/steer":
			var expected string
			json.Unmarshal(r.Params["expectedTurnId"], &expected)
			if expected != turn || state != "active" {
				os.Exit(98)
			}
			reply(map[string]any{"turnId": turn})
		case "turn/interrupt":
			var expected string
			json.Unmarshal(r.Params["turnId"], &expected)
			if expected != turn {
				os.Exit(99)
			}
			state = "idle"
			notify("turn/completed", map[string]any{"threadId": "fixture-thread", "turn": map[string]any{"id": turn, "status": "interrupted"}})
			reply(map[string]any{})
		case "thread/read":
			status := "completed"
			threadStatus := map[string]any{"type": state}
			if state == "active" {
				status = "inProgress"
				if switchFailures && count == 1 {
					threadStatus["activeFlags"] = []string{"waitingOnUserInput"}
				}
			}
			reply(map[string]any{"thread": map[string]any{"id": "fixture-thread", "cwd": cwd, "status": threadStatus, "turns": []any{map[string]any{"id": turn, "status": status}}}})
		default:
			os.Exit(100)
		}
	}
}

func TestM2InstalledCodexSession(t *testing.T) {
	t.Parallel()

	var fixture struct {
		Input                     string
		CancelQuote               string `json:"cancel_quote"`
		Version, Result, Artifact string
	}
	data, err := os.ReadFile("../m2/codex-session.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	var calls atomic.Int32
	var control map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		names := map[string]bool{}
		for _, raw := range req.Tools {
			var d struct{ Name string }
			json.Unmarshal(raw, &d)
			names[d.Name] = true
		}
		for _, name := range []string{"worker_start", "worker_input", "worker_interrupt", "worker_snapshot"} {
			if !names[name] {
				t.Errorf("Core did not offer %s", name)
				emitResponse(w, "missing worker tool", true)
				return
			}
		}
		n := calls.Add(1)
		if n > 1 {
			var item struct{ Output string }
			json.Unmarshal(req.Input[len(req.Input)-1], &item)
			var output struct {
				Code   string
				Output json.RawMessage
			}
			json.Unmarshal([]byte(item.Output), &output)
			// loop results use output as structured JSON inside the provider item.
			if output.Code != "ok" {
				t.Errorf("worker tool failed: %s", item.Output)
				emitResponse(w, "tool failed", true)
				return
			}
			var result map[string]any
			if err := json.Unmarshal(output.Output, &result); err != nil {
				t.Error(err)
				return
			}
			control = map[string]any{"session": result["session"], "turn": result["turn"], "generation": result["generation"], "binding": result["binding"]}
			if result["version"] != fixture.Version || result["verified"] != false {
				t.Errorf("compatibility missing: %+v", result)
			}
			if n == 5 && result["state"] != "idle" {
				t.Errorf("interrupt not observed: %+v", result)
			}
			if n == 7 && (result["state"] != "idle" || result["output"] != fixture.Result) {
				t.Errorf("worker result missing: %+v", result)
			}
		}
		emit := func(name string, args map[string]any) {
			emitPersonaCall(w, fmt.Sprintf("worker-call-%d", n), name, args)
		}
		switch n {
		case 1:
			emit("worker_start", map[string]any{"workdir": workdir, "task_packet": "hold for input"})
		case 2:
			control["text"] = "steer the active turn"
			emit("worker_input", control)
		case 3:
			control["quote"] = fixture.CancelQuote
			emit("worker_interrupt", control)
		case 4:
			emit("worker_snapshot", map[string]any{"session": control["session"]})
		case 5:
			control["text"] = "produce result"
			emit("worker_input", control)
		case 6:
			emit("worker_snapshot", map[string]any{"session": control["session"]})
		default:
			emitResponse(w, "worker journey complete", true)
		}
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	start := func() *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=1", "CODEX_HOME=/fixture/codex-home", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connect(p).Close()
		return p
	}
	core := start()
	c, conversation := conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "worker-journey", fixture.Input)
	if calls.Load() != 7 {
		t.Fatalf("provider steps=%d", calls.Load())
	}
	foundVersion, foundResult := false, false
	for _, frame := range frames {
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil {
			foundVersion = foundVersion || strings.Contains(*item.Text, fixture.Version)
			foundResult = foundResult || strings.Contains(*item.Text, fixture.Result)
		}
	}
	if !foundVersion || !foundResult {
		t.Fatalf("conversation notices version=%t result=%t", foundVersion, foundResult)
	}
	artifact, err := os.ReadFile(filepath.Join(workdir, fixture.Artifact))
	if err != nil || string(artifact) != fixture.Result {
		t.Fatalf("worker artifact=%q %v", artifact, err)
	}
	c.Close()
	h.stopCore(core)
	core = start()
	c, conversation = conversationClient(t, h)
	topic := "conversation:" + conversation
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	response, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(response.Payload)
	if !strings.Contains(string(raw), fixture.Version) || !strings.Contains(string(raw), fixture.Result) {
		t.Fatalf("worker notices not durable: %s", raw)
	}
	c.Close()
	h.stopCore(core)
}

func TestM2InstalledCodexLive(t *testing.T) {
	if os.Getenv("LINA_CODEX_LIVE") != "1" {
		t.Skip("explicit installed-worker acceptance only")
	}
	workdir := t.TempDir()
	finished := make(chan struct{})
	var calls atomic.Int32
	var session string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid model request")
			return
		}
		n := calls.Add(1)
		if n > 1 {
			var item struct{ Output string }
			json.Unmarshal(req.Input[len(req.Input)-1], &item)
			var result struct {
				Code   string
				Output struct {
					Session, Version, State, Output string
					Verified                        bool
				}
			}
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil || result.Code != "ok" {
				t.Errorf("worker tool result failed: %s", item.Output)
				emitResponse(w, "failed", true)
				return
			}
			session = result.Output.Session
			if n == 2 {
				t.Logf("Core launched installed Codex %s verified=%t", result.Output.Version, result.Output.Verified)
			}
			if n == 3 && (result.Output.State != "idle" || result.Output.Output != "LINA_CODEX_OK") {
				t.Errorf("worker result state=%s output=%q", result.Output.State, result.Output.Output)
			}
		}
		switch n {
		case 1:
			emitPersonaCall(w, "live-start", "worker_start", map[string]any{"workdir": workdir, "task_packet": "Reply with exactly LINA_CODEX_OK. Do not use any tools."})
		case 2:
			select {
			case <-finished:
			case <-r.Context().Done():
				return
			}
			emitPersonaCall(w, "live-snapshot", "worker_snapshot", map[string]any{"session": session})
		default:
			emitResponse(w, "live worker result received", true)
		}
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	// This opt-in preserves the actual installed worker's home, PATH and setup.
	// Core still uses its private state and a synthetic local conversation route.
	core.command.Env = append(os.Environ(), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	h.connect(core).Close()
	c, conversation := conversationClient(t, h)
	inputID, text := "live-codex", "Ask the installed Codex worker to return the requested marker."
	if _, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &inputID, Text: &text}); err != nil {
		t.Fatal(err)
	}
	observed := false
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Error != nil {
			t.Fatalf("Core refused: %s", frame.Error.Code)
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem && strings.Contains(*item.Text, "LINA_CODEX_OK") && !observed {
			observed = true
			close(finished)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	if !observed || calls.Load() != 3 {
		t.Fatalf("live result=%t provider calls=%d", observed, calls.Load())
	}
	c.Close()
	h.stopCore(core)
}
