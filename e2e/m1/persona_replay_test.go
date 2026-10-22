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

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestPersonaReplayExactSources(t *testing.T) {
	t.Parallel()

	type request struct {
		Input, Tools []json.RawMessage
		Instructions string
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
		if n == 1 {
			emitPersonaCall(w, "relationship", "record_relationship", map[string]any{"quote": "We are study partners."})
			return
		}
		if n == 4 {
			emitPersonaCall(w, "later-emotion", "record_emotion", map[string]any{"quote": "We finished the test run.", "valence": 0.8, "arousal": 0.4})
			return
		}
		if n == 5 {
			var result struct{ Output string }
			if json.Unmarshal(req.Input[len(req.Input)-1], &result) != nil || !strings.Contains(result.Output, `"code":"ok"`) {
				t.Error("later emotion mutation failed")
			}
		}
		emitResponse(w, "Acknowledged.", true)
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+os.Getenv("PATH"), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	h.connect(core).Close()
	c, conversation := conversationClient(t, h)
	agent := c.Welcome.Agents[0].AgentID
	admittedTurn(t, h, c, conversation, "replay-relationship", "We are study partners.")
	admittedTurn(t, h, c, conversation, "replay-persona", "What do you remember?")
	c.Close()
	export := func() (protocol.ExportResult, replay.Manifest) {
		t.Helper()
		p := h.command("export", "--socket", h.socket, "--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
		p.start(t)
		p.wait(t, 0)
		var out protocol.ExportResult
		if err := json.Unmarshal(p.stdout.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var manifest replay.Manifest
		if err := json.Unmarshal([]byte(out.Manifest), &manifest); err != nil {
			t.Fatal(err)
		}
		return out, manifest
	}
	mu.Lock()
	original := requests[2]
	mu.Unlock()
	for _, marker := range []string{"skill:persona-change", "persona_revision", "persona_emotion", "persona_expression", "persona_settings", "relationship_view"} {
		raw, _ := json.Marshal(original)
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("fixture missing %s", marker)
		}
	}
	out, m := export()
	if m.Exported != 2 || m.LeftOut != 0 {
		t.Fatalf("persona replay export: %+v", m)
	}
	data, err := os.ReadFile(filepath.Join(out.Directory, "episodes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var ep replay.Episode
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &ep); err != nil {
		t.Fatal(err)
	}
	// Change today's state after export; replay must keep the old projection.
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "later-state", "We finished the test run.")
	c.Close()
	p := h.command("replay", ep.TurnID, "--socket", h.socket, "--out", filepath.Join(h.root, "persona-replay"))
	p.start(t)
	p.wait(t, 0)
	mu.Lock()
	got := requests[len(requests)-1]
	count := len(requests)
	mu.Unlock()
	if count != 6 || !reflect.DeepEqual(original, got) {
		t.Fatal("replay did not reproduce the exact persona request")
	}
	// An exported episode cannot revive a subsequently forgotten source.
	memoryID := ""
	for _, source := range ep.Evidence.Assembly.Context {
		if source.Source == "relationship-v1" {
			memoryID = strings.TrimPrefix(source.ID, "memory:")
			if source.Material != nil {
				t.Fatal("personal memory was materialized")
			}
		}
	}
	if memoryID == "" {
		t.Fatal("relationship source missing")
	}
	c, err = clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "memory", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	op := "forget"
	if _, err = c.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &agent, ItemID: &memoryID}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	if err != nil || frame.Error != nil {
		t.Fatalf("forget refused: %+v %v", frame, err)
	}
	c.Close()
	_, m = export()
	if m.Reasons["forgotten"] != 2 || m.Exported != 0 || m.LeftOut != 3 {
		t.Fatalf("forgotten relationship exported: %+v", m)
	}
	p = h.command("replay", filepath.Join(out.Directory, "episodes.jsonl"), "--turn", ep.TurnID, "--socket", h.socket, "--out", filepath.Join(h.root, "forgotten-persona-replay"))
	p.start(t)
	p.wait(t, 0)
	manifest, err := os.ReadFile(filepath.Join(h.root, "forgotten-persona-replay", "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), "forgotten") {
		t.Fatalf("replay forgot exclusion reason: %s %v", manifest, err)
	}
	mu.Lock()
	count = len(requests)
	mu.Unlock()
	if count != 6 {
		t.Fatal("forgotten replay called provider")
	}
	h.stopCore(core)
}
