// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1MemoryQuotesInstalledCore(t *testing.T) {
	var calls atomic.Int32
	const statement = "The desk computer is 자작나무."
	source := statement + " api_key=synthetic-secret-value " + strings.Repeat("증거 본문 ", ledger.InlineBytes/10)
	masked, err := secrets.Mask(source)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			emitPersonaCall(w, "remember-tree", "remember_name", map[string]any{"subject": "desk computer", "current": "자작나무", "quote": statement})
			return
		}
		var last struct{ Output string }
		if len(req.Input) > 0 {
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if !strings.Contains(last.Output, `"remembered":true`) {
			t.Errorf("memory tool did not commit: %s", last.Output)
		}
		emitResponse(w, "Recorded.", true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	agent := c.Welcome.Agents[0].AgentID
	admittedTurn(t, h, c, conversation, "quote-source", source)
	c.Close()
	if calls.Load() != 2 {
		t.Fatalf("tool loop calls=%d", calls.Load())
	}
	read := func() {
		t.Helper()
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "memory", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		op := "list"
		if _, err := c.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &agent}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("memory list: %+v %v", frame, err)
		}
		result, ok := frame.Payload.(protocol.MemoryResult)
		if !ok || result.Items == nil || len(*result.Items) != 1 {
			t.Fatalf("memory result: %+v", frame)
		}
		item := (*result.Items)[0]
		if item.EvidenceDetails == nil || len(*item.EvidenceDetails) != 1 {
			t.Fatalf("missing quote projection: %+v", item)
		}
		ref := (*item.EvidenceDetails)[0]
		if ref.Quote == nil || *ref.Quote != masked.DerivedText() {
			t.Fatal("management quote differs from masked material source")
		}
	}
	read()
	h.stopCore(core)
	db, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memory_item", "memory_revision"} {
		var copies int
		if err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM `+table+` r,json_each(r.record,'$.evidence') e WHERE json_extract(e.value,'$.quote') IS NOT NULL`).Scan(&copies); err != nil {
			t.Fatal(err)
		}
		if copies != 0 {
			t.Fatalf("%s retains %d quotes", table, copies)
		}
	}
	var materials int
	if err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM cite c JOIN conv_event e ON e.id=c.target_id WHERE c.view_kind='memory' AND e.material_id IS NOT NULL AND c.span_start=0 AND c.span_end=?`, len(masked.DerivedText())).Scan(&materials); err != nil {
		t.Fatal(err)
	}
	if materials != 1 {
		t.Fatalf("material span count=%d", materials)
	}
	db.Close()
	core = start(true)
	read()
	h.stopCore(core)
}
