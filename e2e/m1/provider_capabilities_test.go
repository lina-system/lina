// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/provider"
)

// A real installed Core executes a tool and restores the conversation after
// restart. Every captured wire request must have matching pre-send evidence.
func TestM1ProviderCapabilitiesAcrossToolAndRestart(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var requests []provider.Request
	var root string
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Model        provider.ModelRef
			Instructions string
			Input, Tools []json.RawMessage
			Include      []string
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			return
		}
		request := provider.Request{Model: wire.Model, Instructions: wire.Instructions, Items: wire.Input, Tools: wire.Tools, Purpose: provider.Turn}
		mu.Lock()
		requests = append(requests, request)
		n := len(requests)
		mu.Unlock()
		db, err := canon.OpenReader(r.Context(), filepath.Join(root, "canon/canon.sqlite"))
		if err != nil {
			t.Error(err)
			return
		}
		var evidence ledger.RequestEvidence
		var raw, material string
		var revision int64
		err = db.Read(r.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(r.Context(), "SELECT coalesce(body,''),coalesce(material_id,''),coalesce(material_revision,0) FROM conv_event WHERE scaffold_kind='request_assembly' ORDER BY rowid DESC LIMIT 1").Scan(&raw, &material, &revision)
		})
		if err == nil && material != "" {
			var store *assets.Store
			store, err = assets.Open(r.Context(), root, db)
			if err == nil {
				var body []byte
				body, err = store.Read(r.Context(), material, revision)
				raw = string(body)
				_ = store.Close()
			}
		}
		if err == nil {
			err = json.Unmarshal([]byte(raw), &evidence)
		}
		_ = db.Close(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		if evidence.Layout == nil || evidence.Capabilities == nil || !evidence.Capabilities.ReasoningState || !provider.ValidCacheIdentity(evidence.CacheIdentity) {
			t.Errorf("missing pre-send metadata: %+v", evidence)
			return
		}
		request.CacheIdentity, request.Layout, request.Capabilities = evidence.CacheIdentity, evidence.Layout, *evidence.Capabilities
		request.Fields = evidence.Fields
		if assemble.Digest(request) != evidence.Hash {
			t.Error("wire differs from recorded assembly")
		}
		if len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" {
			t.Error("reasoning capability not applied")
		}
		mu.Lock()
		requests[n-1] = request
		mu.Unlock()
		switch n {
		case 1:
			emitPersonaCall(w, "capability-tool", "skill_index", struct{}{})
		case 2:
			var result struct{ Output string }
			if json.Unmarshal(wire.Input[len(wire.Input)-1], &result) != nil || result.Output == "" {
				t.Error("no tool result")
			}
			var output struct{ Code string }
			if json.Unmarshal([]byte(result.Output), &output) != nil || output.Code != "ok" {
				t.Errorf("tool did not run: %s", result.Output)
			}
			emitResponse(w, "Skills checked.", true)
		case 3:
			emitResponse(w, "Conversation restored.", true)
		default:
			t.Error("unexpected provider call")
		}
	})))
	t.Cleanup(server.Close)
	h, start := installedConversationHarness(t, server.URL)
	root = h.root
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "capabilities-first", "Check the available skills.")
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, restored := conversationClient(t, h)
	if restored != conversation {
		t.Fatal("conversation changed")
	}
	admittedTurn(t, h, c, conversation, "capabilities-next", "Continue after restart.")
	c.Close()
	h.stopCore(core)
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests=%d", len(requests))
	}
	first, follow, next := requests[0], requests[1], requests[2]
	if first.Layout == nil || follow.Layout == nil || next.Layout == nil {
		t.Fatal("missing request layouts")
	}
	if first.CacheIdentity != follow.CacheIdentity || first.CacheIdentity != next.CacheIdentity {
		t.Fatal("cache identity changed across tool/restart")
	}
	if first.Layout.PreviousInput != nil || first.Layout.Newest != nil || follow.Layout.Newest == nil || follow.Layout.Newest.Items != len(follow.Items) {
		t.Fatal("wrong turn boundaries")
	}
	if next.Layout.PreviousInput == nil {
		t.Fatal("previous input missing after restart")
	}
	var previous struct{ Content string }
	_ = json.Unmarshal(next.Items[next.Layout.PreviousInput.Items-1], &previous)
	if previous.Content != "Check the available skills." {
		t.Fatal("previous boundary is not the person's input")
	}
}
