// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM4LoadedToolsReplayThroughInstalledCore(t *testing.T) {
	t.Parallel()
	type request struct {
		Instructions string
		Input, Tools []json.RawMessage
	}
	var mu sync.Mutex
	var requests []request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		n := len(requests)
		mu.Unlock()
		if n == 1 || n == 3 {
			// Deliberately reverse registry order. Replay must reproduce the live
			// native declarations and enabled_tools, not the caller's request order.
			emitPersonaCall(w, "load", "load_tools", map[string]any{"tools": []string{"worker_snapshot", "worker_read_file"}})
		} else {
			methodologyOutput(t, methodologyRequest{Input: req.Input}, "ok", nil)
			for _, name := range []string{"worker_snapshot", "worker_read_file"} {
				found := false
				for _, raw := range req.Tools {
					var d struct{ Name string }
					_ = json.Unmarshal(raw, &d)
					found = found || d.Name == name
				}
				if !found || !strings.Contains(req.Instructions, `"name":"`+name+`"`) {
					t.Errorf("loaded declaration missing: %s", name)
				}
			}
			emitResponse(w, "Tools are available.", true)
		}
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+os.Getenv("PATH"), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	h.connect(core).Close()
	waitCoreReady(t, h)
	client, conversation := conversationClient(t, h)
	agent := client.Welcome.Agents[0].AgentID
	text, key := "Load the worker inspection tools.", "load-tools"
	if _, err := client.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text}); err != nil {
		t.Fatal(err)
	}
	active := false
	for {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Error != nil {
			t.Fatalf("input: %+v", frame.Error)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil {
			if *state.Active {
				active = true
			} else if active {
				break
			}
		}
	}
	client.Close()
	export := h.command("export", "--socket", h.socket, "--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
	export.start(t)
	export.wait(t, 0)
	var exported protocol.ExportResult
	if err := json.Unmarshal(export.stdout.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(exported.Directory, "episodes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var ep replay.Episode
	if err = json.Unmarshal(raw, &ep); err != nil {
		t.Fatalf("episode: %s %v", raw, err)
	}
	out := filepath.Join(h.root, "loaded-tools-replay")
	command := h.command("replay", ep.TurnID, "--socket", h.socket, "--out", out)
	command.start(t)
	command.wait(t, 0)
	raw, err = os.ReadFile(filepath.Join(out, "reply.json"))
	if err != nil || !strings.Contains(string(raw), "Tools are available.") {
		t.Fatalf("replayed reply: %s %v", raw, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("requests=%d", len(requests))
	}
	if !reflect.DeepEqual(requests[1].Tools, requests[3].Tools) || requests[1].Instructions != requests[3].Instructions {
		t.Fatal("replay follow-up declarations differ from live Core")
	}
	h.stopCore(core)
}
