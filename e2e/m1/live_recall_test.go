// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1LiveTopicRecallInstalledCore(t *testing.T) {
	const fact = "The observatory telescope is violet."
	var memoryID string
	var requests, reads, expands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests.Add(1)
		var last struct {
			Type, CallID, Output string
			Call                 string `json:"call_id"`
		}
		if len(req.Input) > 0 {
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if last.Type == "function_call_output" {
			if !strings.Contains(last.Output, `"code":"ok"`) {
				t.Errorf("read tool failed: %s", last.Output)
			}
			switch last.Call {
			case "recall-topic":
				reads.Add(1)
				if !strings.Contains(last.Output, memoryID) {
					t.Error("recall omitted memory identity")
				}
				emitPersonaCall(w, "expand-topic", "expand", map[string]any{"id": "node:topic-summary", "limit": 1})
			case "expand-topic":
				expands.Add(1)
				if !strings.Contains(last.Output, fact) {
					t.Error("expand omitted exact source")
				}
				emitResponse(w, "The telescope is violet.", true)
			default:
				t.Errorf("unexpected call %q", last.Call)
				emitResponse(w, "Unexpected call.", true)
			}
			return
		}
		matched := false
		noMatch := false
		for _, raw := range req.Input {
			var item struct{ Content string }
			_ = json.Unmarshal(raw, &item)
			if item.Content == "unrelated-zebras" {
				noMatch = true
			}
			if item.Content == "Tell me about observatory" {
				noMatch = false
			}
			if strings.Contains(item.Content, "topic-memory:") && strings.Contains(item.Content, fact) {
				matched = true
			}
		}
		if noMatch {
			if matched {
				t.Error("no-match turn included topic memory")
			}
			emitResponse(w, "No matching topic.", true)
			return
		}
		if !matched {
			t.Error("first request omitted topic packet")
		}
		names := map[string]bool{}
		for _, raw := range req.Tools {
			var d struct{ Name string }
			_ = json.Unmarshal(raw, &d)
			names[d.Name] = true
		}
		if !names["recall"] || !names["expand"] {
			t.Error("live read family missing")
		}
		emitPersonaCall(w, "recall-topic", "recall", map[string]any{"query": "Tell me about observatory", "kinds": []string{"memory"}})
	}))
	defer server.Close()
	h, _ := installedConversationHarnessWithin(t, 2*time.Minute, server.URL)
	start := func(_ bool) *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "PATH="+os.Getenv("PATH"), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		return core
	}
	core := start(true)
	c, conversation := conversationClient(t, h)
	agent := c.Welcome.Agents[0].AgentID
	// Discover the authenticated person from the disposable install's ledger.
	admittedTurn(t, h, c, conversation, "identity", "unrelated-zebras")
	c.Close()
	h.stopCore(core)
	db, closeStore := installedAcceptanceStore(t, h)
	var person string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT person_id FROM conv_event WHERE agent_id=? AND speaker_kind='person' LIMIT 1", agent).Scan(&person)
	}); err != nil {
		t.Fatal(err)
	}
	journal := ledger.New(db, nil)
	event, err := journal.Admit(h.ctx, guard.EntryOwner, ledger.Input{ID: "topic-source", ConversationID: "earlier-topic", PersonID: person, AgentID: agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Now(), RecordedAt: time.Now(), TimeQuality: "observed", Body: fact})
	if err != nil {
		t.Fatal(err)
	}
	item, err := coreexec.NewMemory(db, nil, nil).Remember(h.ctx, memory.Write{Person: person, Agent: agent, Item: memory.Item{Kind: memory.Fact, Content: fact, Subject: memory.Subject{Kind: memory.Person, ID: person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	memoryID = item.ID
	summaries := compaction.New(db)
	frontier, err := summaries.Frontier(h.ctx, event.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summaries.Publish(h.ctx, compaction.Proposal{Expected: frontier, ID: "topic-summary", Cover: []compaction.Coverage{{EventID: event.ID}}, Output: compaction.Output{Model: "fixture", PromptVersion: "1", BuiltAt: time.Now(), Text: "Observatory equipment.", Dropped: "none"}}); err != nil {
		t.Fatal(err)
	}
	closeStore()
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "topic-turn", "Tell me about observatory")
	c.Close()
	if reads.Load() != 1 || expands.Load() != 1 {
		t.Fatalf("reads=%d expands=%d", reads.Load(), expands.Load())
	}
	// Reconstruct from canon after a restart; no volatile packet survives.
	h.stopCore(core)
	core = start(true)
	export := h.command("export", "--socket", h.socket, "--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
	export.start(t)
	export.wait(t, 0)
	var result protocol.ExportResult
	if err := json.Unmarshal(export.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var manifest replay.Manifest
	if err := json.Unmarshal([]byte(result.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Exported != 2 || manifest.LeftOut != 0 {
		t.Fatalf("export failed digest reconstruction: %+v", manifest)
	}
	raw, err := os.ReadFile(filepath.Join(result.Directory, "episodes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var episode replay.Episode
	found := false
	topicTurn := ""
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if err := json.Unmarshal(line, &episode); err != nil {
			t.Fatal(err)
		}
		for _, ref := range episode.Evidence.Assembly.Context {
			if ref.ID == "memory:"+memoryID && ref.Source == "topic-memory-v1" && ref.Revision == "1" {
				found = true
				topicTurn = episode.TurnID
			}
		}
	}
	if !found {
		t.Fatal("export omitted topic revision")
	}
	// Core's replay command verifies the original request digest before dispatch.
	run := h.command("replay", filepath.Join(result.Directory, "episodes.jsonl"), "--turn", topicTurn, "--socket", h.socket, "--out", filepath.Join(h.root, "topic-replay"))
	run.start(t)
	run.wait(t, 0)
	raw, err = os.ReadFile(filepath.Join(h.root, "topic-replay", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.LeftOut != 0 {
		t.Fatalf("replay excluded topic: %+v", manifest)
	}
	if reads.Load() != 2 || expands.Load() != 2 {
		t.Fatalf("replayed reads=%d expands=%d", reads.Load(), expands.Load())
	}
	h.stopCore(core)
}
