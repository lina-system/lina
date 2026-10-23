// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func seedReplyMethodAgents(t *testing.T, h *harness, person string) map[string]string {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ids := map[string]string{}
	for _, p := range []agents.Preset{
		{ID: "reply-card-fixture", Core: "Synthetic card agent", DefaultVoice: "Plain", WorkMethod: `{"schema":1,"reply":{"comparison":"verdict-then-card","candidates":1},"fill":["reply.comparison"]}`},
		{ID: "reply-table-fixture", Core: "Synthetic table agent", DefaultVoice: "Plain", WorkMethod: `{"schema":1,"reply":{"comparison":"verdict-then-table","candidates":3}}`},
	} {
		if err := store.UpdatePreset(h.ctx, p, person); err != nil {
			t.Fatal(err)
		}
		a, err := store.Create(h.ctx, agents.Create{Name: p.ID, PersonID: person, Preset: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		ids[p.ID] = a.ID
	}
	return ids
}

func TestReplyMethodsAgentIsolationAndRestart(t *testing.T) {
	t.Parallel()
	table := json.RawMessage(`{"columns":[{"name":"Choice","type":"text","unit":""}],"rows":[["A"],["B"]]}`)
	largeTable := json.RawMessage(`{"columns":[{"name":"Choice","type":"text","unit":""}],"rows":[["A"],["B"],["C"],["D"],["E"]]}`)
	draft := json.RawMessage(`{"recipient":"Client","blocks":[{"kind":"paragraph","text":"Please sign.","audience":"recipient"},{"kind":"paragraph","text":"Private note.","audience":"person"}],"from_summary":null}`)
	legacy := json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"Private note."}],"from_summary":null}`)
	type scenario struct {
		name, agent, typ, summary string
		kind                      result.Type
		body                      json.RawMessage
		inline, fill, budget      bool
		breaches                  []string
		comparison                string
	}
	cases := []scenario{
		{name: "card", agent: "reply-card-fixture", kind: result.TableType, summary: "A fits.", body: table, comparison: "verdict-then-card"},
		{name: "table", agent: "reply-table-fixture", kind: result.TableType, summary: "A fits.", body: table, inline: true, comparison: "verdict-then-table"},
		{name: "candidate-breach", agent: "reply-card-fixture", kind: result.TableType, typ: "candidates", summary: "Choose A.", body: table, breaches: []string{"candidates"}, comparison: "verdict-then-card"},
		{name: "three-allowed", agent: "reply-table-fixture", kind: result.TableType, typ: "candidates", summary: "Choose A.", body: table, comparison: "verdict-then-table"},
		{name: "status", agent: "reply-card-fixture", kind: result.TableType, typ: "status", summary: "Ready.\nBlocked.\nNext.\nLater.", body: table, breaches: []string{"status"}, budget: true, comparison: "verdict-then-card"},
		{name: "recipient", agent: "reply-table-fixture", kind: result.DraftType, summary: "Draft ready.", body: draft, breaches: []string{"third_party"}, comparison: "verdict-then-table"},
		{name: "unassessed", agent: "reply-card-fixture", kind: result.DraftType, summary: "Draft ready.", body: legacy, comparison: "verdict-then-card"},
		{name: "large-table", agent: "reply-table-fixture", kind: result.TableType, summary: "A fits.", body: largeTable, comparison: "verdict-then-table"},
		{name: "comparison-breach", agent: "reply-table-fixture", kind: result.TableType, summary: "A fits.\nB costs more.", body: table, inline: true, breaches: []string{"comparison"}, comparison: "verdict-then-table"},
		{name: "fill-pinned-turn", agent: "reply-card-fixture", kind: result.TableType, summary: "A fits.", body: table, fill: true, comparison: "verdict-then-card"},
		{name: "fill-next-turn", agent: "reply-card-fixture", kind: result.TableType, summary: "A fits.", body: table, inline: true, comparison: "verdict-then-table"},
	}
	const quote = "Use a short inline table for comparisons."
	var active, stage, calls atomic.Int32
	receipts := make(chan result.Card, 1)
	prefixes := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		tc := cases[int(active.Load())]
		var req struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			emitResponse(w, "Failed.", true)
			return
		}
		n := stage.Add(1)
		if n == 1 {
			if previous := prefixes[tc.agent]; previous != "" && previous != req.Instructions {
				t.Error("fill changed cached instruction prefix")
			}
			prefixes[tc.agent] = req.Instructions
			for _, raw := range req.Tools {
				var tool struct{ Name string }
				_ = json.Unmarshal(raw, &tool)
				if tool.Name == "save_result" {
					t.Error("presentation exposed saving without person intent")
				}
			}
		}
		presentAt := int32(1)
		if tc.fill {
			presentAt = 2
		}
		if tc.fill && n == 1 {
			emitPersonaCall(w, "fill-reply", "fill_work_method", map[string]any{"quote": quote, "key": "reply.comparison", "value": "verdict-then-table"})
			return
		}
		if n > 1 {
			var item struct{ Output string }
			if len(req.Input) == 0 || json.Unmarshal(req.Input[len(req.Input)-1], &item) != nil {
				t.Error("missing tool acknowledgement")
			}
			var out struct {
				Code   string
				Output json.RawMessage
			}
			if err := json.Unmarshal([]byte(item.Output), &out); err != nil || out.Code != "ok" {
				t.Errorf("tool result: %s (%v)", item.Output, err)
			}
			if n == presentAt+1 {
				var card result.Card
				if err := json.Unmarshal(out.Output, &card); err != nil {
					t.Error(err)
				}
				receipts <- card
				emitResponse(w, "Presented.", true)
				return
			}
		}
		if n == presentAt {
			p := result.PresentRequest{Type: tc.kind, ReplyType: tc.typ, Title: tc.name, Summary: tc.summary, Body: tc.body}
			emitPersonaCall(w, "result-"+tc.name, "present_result", p)
			return
		}
		t.Errorf("unexpected provider round %d", n)
		emitResponse(w, "Failed.", true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, _ := conversationClient(t, h)
	person := c.Welcome.PersonID
	c.Close()
	h.stopCore(core)
	ids := seedReplyMethodAgents(t, h, person)
	core = start(true)
	c, _ = conversationClient(t, h)
	expected := map[string]protocol.Kind{}
	resultIDs := map[string]string{}
	subscribed := map[string]bool{}
	for i, tc := range cases {
		active.Store(int32(i))
		stage.Store(0)
		conversation := "main:" + ids[tc.agent]
		if !subscribed[conversation] {
			topic := "conversation:" + conversation
			if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			if frame, err := c.Receive(h.ctx); err != nil || frame.Kind != protocol.KindSnapshot {
				t.Fatal("missing agent snapshot", err)
			}
			subscribed[conversation] = true
		}
		prompt := "CASE " + tc.name
		if tc.fill {
			prompt = quote
		}
		frames := admittedTurn(t, h, c, conversation, tc.name, prompt)
		var card result.Card
		select {
		case card = <-receipts:
		default:
			t.Fatal("completed turn has no typed result acknowledgement", tc.name)
		}
		if card.ID == "" {
			t.Fatal("empty result identity", tc.name)
		}
		resultIDs[tc.name] = card.ID
		want := protocol.KindResult
		if tc.inline {
			want = protocol.KindItemDone
		}
		expected["typed-result:"+card.ID] = want
		found := false
		for _, frame := range frames {
			if value, ok := frame.Payload.(protocol.Result); ok && value.ResultID == card.ID {
				found = true
				if tc.inline || value.Provenance.AgentID != ids[tc.agent] || value.Text == nil || !strings.Contains(*value.Text, tc.summary) {
					t.Errorf("wrong result card for %s: %+v", tc.name, value)
				}
			}
			if value, ok := frame.Payload.(protocol.ItemDone); ok && value.Text != nil && strings.HasPrefix(*value.Text, tc.summary+"\nChoice") {
				found = true
				if !tc.inline {
					t.Errorf("comparison escaped its card: %s", tc.name)
				}
			}
		}
		if !found {
			t.Errorf("typed presentation missing: %s", tc.name)
		}
	}
	checkMeasurements := func() int {
		t.Helper()
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, tc := range cases {
			var raw string
			err := db.QueryRowContext(h.ctx, `SELECT e.body FROM result_heads h JOIN conv_event source ON source.id=h.event_id JOIN conv_event e ON e.parent_id=source.id WHERE h.id=? AND e.scaffold_kind='reply_check' AND e.agent_id=? AND source.agent_id=e.agent_id`, resultIDs[tc.name], ids[tc.agent]).Scan(&raw)
			if err != nil {
				t.Fatal(err)
			}
			var check reply.Check
			if err := json.Unmarshal([]byte(raw), &check); err != nil {
				t.Fatal(err)
			}
			if check.Shapes == nil || check.Shapes.Comparison != tc.comparison || !reflect.DeepEqual(check.ShapeBreaches, tc.breaches) || check.BudgetBreach != tc.budget {
				t.Errorf("%s measurement=%+v", tc.name, check)
			}
			if check.Shapes != nil && check.Shapes.Candidates != map[string]int{"reply-card-fixture": 1, "reply-table-fixture": 3}[tc.agent] {
				t.Error("other agent's candidate count leaked")
			}
		}
		for agent, want := range map[string]map[string]int{
			"reply-card-fixture":  {"status": 1, "comparison": 0, "candidates": 1, "third_party": 0},
			"reply-table-fixture": {"status": 0, "comparison": 1, "candidates": 0, "third_party": 1},
		} {
			for kind, n := range want {
				var got int
				err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM conv_event e,json_each(e.body,'$.shape_breaches') b WHERE e.scaffold_kind='reply_check' AND e.agent_id=? AND b.value=?`, ids[agent], kind).Scan(&got)
				if err != nil {
					t.Fatal(err)
				}
				if got != n {
					t.Errorf("%s %s breaches=%d want %d", agent, kind, got, n)
				}
			}
		}
		var total int
		if err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM conv_event WHERE scaffold_kind='reply_check'`).Scan(&total); err != nil {
			t.Fatal(err)
		}
		return total
	}
	before := checkMeasurements()
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, _ = conversationClient(t, h)
	restored := map[string]protocol.Kind{}
	for _, id := range ids {
		topic := "conversation:main:" + id
		if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok {
			t.Fatalf("expected snapshot: %T", frame.Payload)
		}
		for _, item := range snapshot.Items {
			if strings.HasPrefix(item.Key, "typed-result:") {
				restored[item.Key] = item.Kind
			}
		}
	}
	if !reflect.DeepEqual(restored, expected) {
		t.Errorf("restart changed pinned presentation: got %v want %v", restored, expected)
	}
	if after := checkMeasurements(); after != before {
		t.Errorf("restart recounted: %d -> %d", before, after)
	}
	c.Close()
	h.stopCore(core)
	if calls.Load() != int32(len(cases)*2+1) {
		t.Fatal(fmt.Sprintf("unexpected provider calls: %d", calls.Load()))
	}
}
