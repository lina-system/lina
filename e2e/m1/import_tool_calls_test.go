// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
)

func TestM1ImportedToolCallsExpandInRunningCore(t *testing.T) {
	t.Parallel()
	const content = "Inspect the archived receipt."
	const arguments = `{"path":"/work/archive-receipt.txt"}`
	var eventID atomic.Value
	var requests atomic.Int32
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if requests.Add(1)%2 == 1 {
			if !toolReachable(req.Tools, "expand") {
				t.Error("running Core did not offer expand")
				emitResponse(w, "Expansion unavailable.", true)
				return
			}
			emitPersonaCall(w, "expand-import", "expand", map[string]any{"id": "event:" + eventID.Load().(string)})
			return
		}
		var result struct{ Output string }
		if err := json.Unmarshal(req.Input[len(req.Input)-1], &result); err != nil {
			t.Error(err)
		}
		var receipt struct {
			Code   string
			Output compaction.ExpansionPage
		}
		if err := json.Unmarshal([]byte(result.Output), &receipt); err != nil || receipt.Code != "ok" || len(receipt.Output.Events) != 1 {
			t.Errorf("expand receipt: %s: %v", result.Output, err)
		} else {
			var body struct {
				Content string
				Calls   []struct {
					Type            string
					ID              string `json:"call_id"`
					Name, Arguments string
				} `json:"tool_calls"`
			}
			if err := json.Unmarshal([]byte(receipt.Output.Events[0].Body), &body); err != nil || body.Content != content || len(body.Calls) != 1 {
				t.Errorf("expanded body: %+v: %v", body, err)
			} else if c := body.Calls[0]; c.Type != "function_call" || c.ID != "archive-read" || c.Name != "read_file" || c.Arguments != arguments {
				t.Errorf("expanded call: %+v", c)
			}
		}
		emitResponse(w, "The archived call read /work/archive-receipt.txt.", true)
	})))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 8*operationTimeout)
	defer cancel()
	h.ctx = ctx
	core := start(true)
	c, _ := conversationClient(t, h)
	c.Close()
	h.stopCore(core)
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var agent, person string
	if err = db.QueryRowContext(h.ctx, "SELECT agent_id FROM agent_first_run").Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(h.ctx, "SELECT person_id FROM agent_revisions WHERE agent_id=? LIMIT 1", agent).Scan(&person); err != nil {
		t.Fatal(err)
	}
	db.Close()
	source := t.TempDir()
	for _, kind := range []string{"lcm", "state"} {
		ddl, err := os.ReadFile("../../testdata/hermes/" + kind + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		src, err := canon.OpenSQLite(h.ctx, filepath.Join(source, kind+".db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = src.ExecContext(h.ctx, string(ddl)); err != nil {
			t.Fatal(err)
		}
		if kind == "lcm" {
			calls, _ := json.Marshal([]any{map[string]any{"id": "archive-read", "type": "function", "function": map[string]string{"name": "read_file", "arguments": arguments}}})
			if _, err = src.ExecContext(h.ctx, "UPDATE messages SET content=?,tool_calls=? WHERE store_id=2", content, string(calls)); err != nil {
				t.Fatal(err)
			}
			if _, err = src.ExecContext(h.ctx, "UPDATE host_uid_bindings SET lineage_key='home:synthetic-session'"); err != nil {
				t.Fatal(err)
			}
		}
		src.Close()
	}
	p := h.command("import-hermes", "--state-root", h.root, "--source", source, "--profile", "fixture", "--agent", agent, "--person", person, "--owner-session", "synthetic-session", "--confirm-import", "--promote", "none")
	p.start(t)
	p.wait(t, 0)
	db, err = canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = db.QueryRowContext(h.ctx, "SELECT record_id FROM legacy_ref WHERE system='hermes-lcm' AND kind='messages' AND key='2'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	db.Close()
	eventID.Store(id)
	for _, key := range []string{"expand-import", "expand-after-restart"} {
		core = start(true)
		c, conversation := conversationClient(t, h)
		admittedTurn(t, h, c, conversation, key, "Read the imported event "+id+" with expand.")
		c.Close()
		h.stopCore(core)
	}
	if requests.Load() != 4 {
		t.Errorf("model requests=%d, want two tool calls and two results", requests.Load())
	}
}
