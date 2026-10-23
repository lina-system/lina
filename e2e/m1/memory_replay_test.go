// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"crypto/sha256"
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
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestM1MemoryReplayInstalledCore(t *testing.T) {
	for _, tc := range []struct {
		name, text, operation, sql, reason string
	}{
		{name: "legacy layout"},
		{name: "profile whitespace", text: "  I prefer\n\tgreen\u00a0 tea.  "},
		{name: "corrected", operation: "correct", reason: "source_changed"},
		{name: "forgotten", operation: "forget", reason: "forgotten"},
		{name: "restricted", sql: `UPDATE memory_item SET scope='private',record=json_set(record,'$.scope','private') WHERE id=?`, reason: "source_changed"},
		{name: "never leaves", sql: `UPDATE memory_item SET scope='never-leaves',record=json_set(record,'$.scope','never-leaves') WHERE id=?`, reason: "source_changed"},
		{name: "changed revision", sql: `UPDATE memory_item SET record=json_set(record,'$.revision',99) WHERE id=?`, reason: "source_changed"},
		{name: "unverifiable bytes", sql: `UPDATE memory_item SET record=json_set(record,'$.content','Unverifiable replacement') WHERE id=?`, reason: "missing_assembly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type request struct {
				Input, Tools        []json.RawMessage
				Instructions, Model string
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
				mu.Unlock()
				emitResponse(w, "Acknowledged.", true)
			}))
			defer server.Close()
			h, _ := installedConversationHarness(t, server.URL)
			start := func() *process {
				core := h.command("core", "--state-root", h.root, "--socket", h.socket)
				core.command.Env = append(core.command.Env, "PATH="+os.Getenv("PATH"), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
				core.start(t)
				h.connect(core).Close()
				waitCoreReady(t, h)
				return core
			}
			core := start()
			c, conversation := conversationClient(t, h)
			agent := c.Welcome.Agents[0].AgentID
			c.Close()
			memory := func(operation, id, text string) protocol.MemoryResult {
				t.Helper()
				c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "memory", Version: "1.0"}})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				req := protocol.MemoryRequest{Operation: &operation, AgentID: &agent}
				if id != "" {
					req.ItemID = &id
				}
				if text != "" {
					req.Text = &text
				}
				if _, err = c.Memory(h.ctx, req); err != nil {
					t.Fatal(err)
				}
				frame, err := c.Receive(h.ctx)
				if err != nil || frame.Error != nil {
					t.Fatalf("memory %s: %+v %v", operation, frame, err)
				}
				result, ok := frame.Payload.(protocol.MemoryResult)
				if !ok {
					t.Fatalf("memory result: %T", frame.Payload)
				}
				return result
			}
			text := tc.text
			if text == "" {
				text = "I prefer green tea."
			}
			first := memory("remember", "", text)
			second := memory("remember", "", "I prefer short replies. api_key=[REDACTED]")
			if first.ItemID == nil || second.ItemID == nil {
				t.Fatal("missing memory identity")
			}
			c, conversation = conversationClient(t, h)
			admittedTurn(t, h, c, conversation, "memory-replay", "Use my preferences. api_key=synthetic-secret-value")
			c.Close()
			mu.Lock()
			original := requests[0]
			mu.Unlock()
			// The live profile is newest first and collapses Unicode whitespace.
			var remembered []string
			for _, raw := range original.Input {
				var item struct{ Content string }
				if json.Unmarshal(raw, &item) == nil && strings.HasPrefix(item.Content, "Remembered statement (") {
					remembered = append(remembered, item.Content)
				}
			}
			want := []string{"Remembered statement (" + *second.ItemID + "): I prefer short replies. api_key=[REDACTED]", "Remembered statement (" + *first.ItemID + "): I prefer green tea."}
			if !reflect.DeepEqual(remembered, want) {
				t.Fatalf("live profile: %q", remembered)
			}
			var legacyEvidence ledger.RequestEvidence
			if tc.name == "legacy layout" {
				// Read evidence through Core, including material-backed large records.
				p := h.command("export", "--socket", h.socket, "--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
				p.start(t)
				p.wait(t, 0)
				var out protocol.ExportResult
				if err := json.Unmarshal(p.stdout.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(out.Directory, "episodes.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				var ep replay.Episode
				if err := json.Unmarshal(bytes.TrimSpace(raw), &ep); err != nil {
					t.Fatal(err)
				}
				legacyEvidence = ep.Evidence
			}
			// Restart proves reconstruction does not depend on transient packet bytes.
			h.stopCore(core)
			if tc.name == "legacy layout" {
				// Model an old install: its profile preceded history, and its
				// input followed the packet. Keep the actual immutable sources.
				var profiles, rest []json.RawMessage
				var current json.RawMessage
				for _, raw := range original.Input {
					var item struct{ Content string }
					_ = json.Unmarshal(raw, &item)
					switch {
					case strings.HasPrefix(item.Content, "Remembered statement ("):
						profiles = append(profiles, raw)
					case strings.HasPrefix(item.Content, "Use my preferences."):
						current = raw
					default:
						rest = append(rest, raw)
					}
				}
				if len(profiles) != 2 || current == nil {
					t.Fatal("legacy fixture missing profile or input")
				}
				original.Input = append(append(append([]json.RawMessage{rest[0]}, profiles...), rest[1:]...), current)
				db, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				evidence := legacyEvidence
				evidence.Assembly.RequestLayout = ""
				legacyRequest := provider.Request{Model: evidence.Model, Effort: evidence.Effort, Purpose: provider.Turn,
					Instructions: original.Instructions, Items: original.Input, Tools: evidence.Tools, Fields: evidence.Fields,
					CacheIdentity: evidence.CacheIdentity, Layout: evidence.Layout.Clone()}
				if evidence.Capabilities != nil {
					legacyRequest.Capabilities = evidence.Capabilities.Clone()
				}
				evidence.Hash = assemble.Digest(legacyRequest)
				raw, err := json.Marshal(evidence)
				if err != nil {
					db.Close()
					t.Fatal(err)
				}
				// Seed an old ledger only while this disposable Core is stopped.
				// Restore the exact append-only trigger in the same transaction.
				tx, err := db.BeginTx(h.ctx, nil)
				if err != nil {
					db.Close()
					t.Fatal(err)
				}
				var trigger string
				err = tx.QueryRowContext(h.ctx, "SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='conv_event_no_update'").Scan(&trigger)
				if err == nil {
					_, err = tx.ExecContext(h.ctx, "DROP TRIGGER conv_event_no_update")
				}
				if err == nil {
					_, err = tx.ExecContext(h.ctx, "UPDATE conv_event SET body=?,material_id=NULL,material_revision=NULL WHERE id=?", string(raw), "request:"+evidence.TurnID+":0")
				}
				if err == nil {
					_, err = tx.ExecContext(h.ctx, trigger)
				}
				if err == nil {
					err = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
				closeErr := db.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("legacy evidence: %v %v", err, closeErr)
				}
			}
			core = start()
			digest := func() [32]byte {
				t.Helper()
				var content []byte
				for _, suffix := range []string{"", "-wal"} {
					b, err := os.ReadFile(filepath.Join(h.root, "canon/canon.sqlite") + suffix)
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					content = append(content, b...)
				}
				return sha256.Sum256(content)
			}
			export := func() (string, replay.Manifest) {
				t.Helper()
				p := h.command("export", "--socket", h.socket, "--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
				p.start(t)
				p.wait(t, 0)
				var out protocol.ExportResult
				if err := json.Unmarshal(p.stdout.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				var m replay.Manifest
				if err := json.Unmarshal([]byte(out.Manifest), &m); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(out.Directory, "episodes.jsonl"), m
			}
			before := digest()
			file, m := export()
			if m.Selected != 1 || m.Exported != 1 || m.LeftOut != 0 {
				t.Fatalf("remembered turn excluded: %+v", m)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("synthetic-secret-value")) {
				t.Fatal("export exposed secret")
			}
			var ep replay.Episode
			if err := json.Unmarshal(bytes.TrimSpace(data), &ep); err != nil {
				t.Fatal(err)
			}
			wantLayout := ledger.InputBeforeContextV1
			if tc.name == "legacy layout" {
				wantLayout = ""
			}
			if ep.Evidence.Assembly.RequestLayout != wantLayout {
				t.Fatalf("wrong recorded layout: %q", ep.Evidence.Assembly.RequestLayout)
			}
			if ep.Evidence.Hash == "" || len(ep.Evidence.Assembly.Profile) != 2 || ep.Evidence.Assembly.Profile[0].ID != *second.ItemID || ep.Evidence.Assembly.Profile[1].ID != *first.ItemID {
				t.Fatal("missing ordered memory assembly")
			}
			rebuilt := provider.Request{Model: ep.Evidence.Model, Effort: ep.Evidence.Effort, Purpose: provider.Turn,
				Instructions: original.Instructions, Items: ep.Items, Tools: ep.Evidence.Tools, Fields: ep.Evidence.Fields,
				CacheIdentity: ep.Evidence.CacheIdentity, Layout: ep.Evidence.Layout.Clone()}
			if ep.Evidence.Capabilities != nil {
				rebuilt.Capabilities = ep.Evidence.Capabilities.Clone()
			}
			if assemble.Digest(rebuilt) != ep.Evidence.Hash {
				t.Fatal("reconstructed request differs from the recorded digest")
			}
			replayTurn := func(input, output string) replay.Manifest {
				t.Helper()
				out := filepath.Join(h.root, output)
				p := h.command("replay", input, "--socket", h.socket, "--out", out)
				p.start(t)
				p.wait(t, 0)
				data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				var m replay.Manifest
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				return m
			}
			for i, input := range []string{ep.TurnID, file} {
				m = replayTurn(input, []string{"turn-replay", "episode-replay"}[i])
				if m.LeftOut != 0 {
					t.Fatalf("replay excluded: %+v", m)
				}
			}
			mu.Lock()
			got := append([]request(nil), requests...)
			mu.Unlock()
			if len(got) != 3 || !reflect.DeepEqual(original, got[1]) || !reflect.DeepEqual(original, got[2]) {
				t.Fatal("replay did not reproduce exact provider request")
			}
			if digest() != before {
				t.Fatal("export/replay changed canon, ledger or effects")
			}
			if tc.operation != "" {
				replacement := ""
				if tc.operation == "correct" {
					replacement = "I prefer black tea."
				}
				memory(tc.operation, *first.ItemID, replacement)
			}
			if tc.sql != "" {
				// Fault injection only in this disposable install, with Core stopped.
				h.stopCore(core)
				db, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.ExecContext(h.ctx, tc.sql, *first.ItemID)
				closeErr := db.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("inject evidence change: %v %v", err, closeErr)
				}
				core = start()
			}
			if tc.reason != "" {
				before = digest()
				_, m = export()
				if m.Exported != 0 || m.LeftOut != 1 || m.Reasons[tc.reason] != 1 {
					t.Fatalf("changed evidence exported: %+v", m)
				}
				for i, input := range []string{ep.TurnID, file} {
					m = replayTurn(input, []string{"excluded-turn", "excluded-episode"}[i])
					if m.LeftOut != 1 || m.Reasons[tc.reason] != 1 {
						t.Fatalf("changed evidence replayed: %+v", m)
					}
				}
				mu.Lock()
				count := len(requests)
				mu.Unlock()
				if count != 3 || digest() != before {
					t.Fatal("excluded export/replay called provider or changed canon")
				}
			}
			h.stopCore(core)
		})
	}
}
