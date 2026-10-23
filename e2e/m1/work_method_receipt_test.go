// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestWorkMethodReceiptThroughInstalledCore(t *testing.T) {
	t.Parallel()

	const quote = "Ask one narrowed question."
	var calls atomic.Int32
	receipts := make(chan coreexec.SettingReceipt, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			emitResponse(w, "Failed.", true)
			return
		}
		n := calls.Add(1)
		switch n {
		case 1, 3:
			declared := false
			for _, raw := range request.Tools {
				var tool struct{ Name string }
				if json.Unmarshal(raw, &tool) == nil && tool.Name == "fill_work_method" {
					declared = true
				}
			}
			if !declared {
				t.Error("work-method tool not offered")
			}
			value := json.RawMessage(`"\u006earrowed"`)
			if n == 3 {
				value = json.RawMessage(`"narrowed"`)
			}
			emitPersonaCall(w, "method-fill", "fill_work_method", map[string]any{"quote": quote, "key": "ask.form", "value": value})
		case 2, 4:
			var receipt coreexec.SettingReceipt
			found := false
			for _, raw := range request.Input {
				var item struct{ Type, Output string }
				if json.Unmarshal(raw, &item) != nil || item.Type != "function_call_output" {
					continue
				}
				var result struct {
					Code   string
					Output coreexec.SettingReceipt
				}
				if err := json.Unmarshal([]byte(item.Output), &result); err != nil || result.Code != "ok" {
					t.Errorf("fill result: %s (%v)", item.Output, err)
					continue
				}
				receipt, found = result.Output, true
			}
			if !found || receipt.Revision == "" || receipt.Filled != (n == 2) {
				t.Errorf("request %d receipt: %+v, found=%v", n, receipt, found)
			}
			receipts <- receipt
			emitResponse(w, "Acknowledged.", true)
		default:
			t.Errorf("unexpected provider request %d", n)
			emitResponse(w, "Failed.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	var firstSource, firstRevision string
	for turn := 0; turn < 2; turn++ {
		core := start(true)
		c, conversation := conversationClient(t, h)
		admittedTurn(t, h, c, conversation, "method-fill-"+strconv.Itoa(turn), quote)
		var receipt coreexec.SettingReceipt
		select {
		case receipt = <-receipts:
		default:
			t.Fatal("completed turn has no tool receipt")
		}
		reader, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reader.Close() })
		var agent string
		var raw []byte
		if err := reader.QueryRowContext(h.ctx, "SELECT a.id,a.state FROM agents a JOIN agent_first_run f ON a.id=f.agent_id WHERE f.singleton=1").Scan(&agent, &raw); err != nil {
			t.Fatal(err)
		}
		var state agents.State
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		fill := state.WorkMethodFills["ask.form"]
		if string(fill.Value) != `"narrowed"` || fill.Source == "" {
			t.Fatalf("persisted fill: %+v", fill)
		}
		want := agents.StateRevision(state)
		if receipt.Revision != want {
			t.Errorf("tool receipt revision %s != stored revision %s", receipt.Revision, want)
		}
		var targetRevision string
		if err := reader.QueryRowContext(h.ctx, "SELECT revision FROM authority_targets WHERE target=?", "persona-state:"+agent).Scan(&targetRevision); err != nil {
			t.Fatal(err)
		}
		if targetRevision != want {
			t.Errorf("authority target revision %s != stored revision %s", targetRevision, want)
		}
		rows, err := reader.QueryContext(h.ctx, "SELECT record FROM authority_effects WHERE json_extract(record,'$.Tool')='fill_work_method'")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var effect authority.Effect
			if err := json.Unmarshal(raw, &effect); err != nil {
				t.Fatal(err)
			}
			var body coreexec.SettingReceipt
			if err := json.Unmarshal(effect.Result.Body, &body); err != nil {
				t.Fatal(err)
			}
			if effect.State != tools.Applied || effect.Result.Revision != want || body.Revision != want {
				t.Errorf("durable receipt differs from state: %+v", effect.Result)
			}
			count++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if count != turn+1 {
			t.Errorf("effect count=%d, want %d", count, turn+1)
		}
		var speaker, person, sourceAgent string
		if err := reader.QueryRowContext(h.ctx, "SELECT speaker_kind,person_id,agent_id FROM conv_event WHERE id=?", fill.Source).Scan(&speaker, &person, &sourceAgent); err != nil {
			t.Fatal(err)
		}
		if speaker != "person" || person != c.Welcome.PersonID || sourceAgent != agent {
			t.Fatal("fill lacks confirmed owner evidence")
		}
		if turn == 0 {
			firstSource, firstRevision = fill.Source, want
		} else if fill.Source != firstSource || want != firstRevision {
			t.Error("equivalent fill after restart changed state or evidence")
		}
		reader.Close()
		c.Close()
		h.stopCore(core)
	}
	if calls.Load() != 4 {
		t.Fatalf("provider calls=%d, want 4", calls.Load())
	}
}
