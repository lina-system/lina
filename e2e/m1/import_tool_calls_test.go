// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/addons/importer/hermes"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestM1ImportedToolCallsInstalled(t *testing.T) {
	t.Parallel()
	const content = "Inspect the archived receipt."
	const arguments = `{"path":"/work/archive-receipt.txt"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("import unexpectedly called a model")
		http.Error(w, "unexpected model request", http.StatusInternalServerError)
	}))
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
	// The installed command publishes into the same canon Core reopens.
	// Read that durable evidence through the existing expansion and summary
	// services; this import test adds no conversation tool or grant surface.
	for range 2 {
		core = start(true)
		c, _ = conversationClient(t, h)
		c.Close()
		readImportedToolCalls(t, h, agent, id, content, arguments)
		h.stopCore(core)
	}
}

func readImportedToolCalls(t *testing.T, h *harness, agent, id, content, arguments string) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	events := ledger.New(db, nil)
	event, err := events.Get(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	check := func(raw string) {
		t.Helper()
		var body struct {
			Content string
			Calls   []struct {
				Type            string
				ID              string `json:"call_id"`
				Name, Arguments string
			} `json:"tool_calls"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil || body.Content != content || len(body.Calls) != 1 {
			t.Fatalf("imported body: %+v %v", body, err)
		}
		if c := body.Calls[0]; c.Type != "function_call" || c.ID != "archive-read" || c.Name != "read_file" || c.Arguments != arguments {
			t.Fatalf("imported call: %+v", c)
		}
	}
	raw, err := (compaction.Expansion{DB: db, Events: events}).Read(h.ctx, agent, "event:"+id, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var page compaction.ExpansionPage
	if err = json.Unmarshal(raw, &page); err != nil || len(page.Events) != 1 {
		t.Fatalf("expanded events: %s %v", raw, err)
	}
	check(page.Events[0].Body)
	var segment compaction.PassSegment
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(h.ctx, "SELECT id FROM conv_event WHERE conversation_id=? AND scaffold_kind IS NULL ORDER BY rowid", event.ConversationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			segment.Records = append(segment.Records, compaction.Record{ID: id})
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	builder := compaction.Builder{Store: compaction.New(db), Events: events, Agent: agent, Conversation: event.ConversationID, Model: "fixture", Now: time.Now, NextID: func() string { return "summary-proof" }, Generate: func(_ context.Context, s compaction.PassSegment) (compaction.Text, error) {
		for _, r := range s.Records {
			if r.ID == id {
				var body struct{ Body string }
				if err := json.Unmarshal(r.Item, &body); err != nil {
					t.Fatal(err)
				}
				check(body.Body)
				found = true
			}
		}
		return compaction.Text{Requests: "none", Topics: "archive", Actions: "read", Values: arguments, Omitted: "none"}, nil
	}}
	if _, err = builder.Summarize(h.ctx, segment); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("summary omitted imported assistant")
	}
	key := hermes.LegacyKey{System: string(hermes.HermesLCM), Instance: "fixture", Kind: "messages", Key: "2"}
	ref, err := hermes.Resolve(h.ctx, db, key)
	if err != nil {
		t.Fatal(err)
	}
	row, err := hermes.Reverse(h.ctx, db, key)
	if err != nil || hermes.RowHash(row) != ref.SourceHash {
		t.Fatalf("source round trip: %v", err)
	}
}
