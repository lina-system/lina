// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TurnBudgetHandoffInstalledCore(t *testing.T) {
	t.Parallel()

	var scenario struct{ Input, Next, Reply, Artifact string }
	data, err := os.ReadFile("turn-budget-handoff.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var foreground, background atomic.Int32
	backgroundStarted, nextStarted := make(chan struct{}, 1), make(chan struct{}, 1)
	finishBackground, finishNext := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input, Tools []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			if background.Add(1) != 1 {
				t.Error("background request replayed")
			}
			gathered := string(mustJSON(t, request.Input))
			for _, marker := range []string{scenario.Input, "read-1", "read-10", "budget_reached"} {
				if !strings.Contains(gathered, marker) {
					t.Errorf("promotion lost %s", marker)
				}
			}
			for _, tool := range request.Tools {
				var decl struct{ Name string }
				if err := json.Unmarshal(tool, &decl); err != nil {
					t.Error(err)
				}
				if decl.Name == "start_run" || decl.Name == "write_skill" {
					t.Error("run can start work or write")
				}
			}
			backgroundStarted <- struct{}{}
			select {
			case <-finishBackground:
			case <-r.Context().Done():
				return
			}
			emitResponse(w, string(mustJSON(t, runs.Artifact{Body: scenario.Artifact, Done: true})), true)
			return
		}
		if strings.Contains(string(mustJSON(t, request.Input)), scenario.Next) {
			nextStarted <- struct{}{}
			select {
			case <-finishNext:
			case <-r.Context().Done():
				return
			}
			emitResponse(w, scenario.Reply, true)
			return
		}
		n := foreground.Add(1)
		if n > 10 {
			t.Error("provider dispatched beyond Hard")
			w.WriteHeader(500)
			return
		}
		if n >= 7 && !strings.Contains(string(mustJSON(t, request.Tools)), "start_run") {
			t.Errorf("Soft request %d does not offer start_run", n)
		}
		emitPersonaCall(w, fmt.Sprintf("read-%d", n), "skill_index", map[string]any{})
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	client, conversation := conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "budget-407", scenario.Input)
	if foreground.Load() != 10 {
		t.Fatalf("foreground requests=%d", foreground.Load())
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err = db.QueryRowContext(h.ctx, "SELECT state FROM turn_runs").Scan(&state); err != nil || state != "completed" {
		t.Fatalf("Hard turn state=%s err=%v", state, err)
	}
	select {
	case <-backgroundStarted:
	case <-h.ctx.Done():
		t.Fatal("no background run", h.ctx.Err())
	}
	var count int
	if err := db.QueryRowContext(h.ctx, "SELECT count(*) FROM background_runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("run count=%d err=%v", count, err)
	}
	var raw []byte
	var record runs.Record
	if err = db.QueryRowContext(h.ctx, "SELECT record FROM background_runs").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Promotion == nil || len(record.Promotion.Results) != 10 {
		t.Fatalf("missing promotion evidence: %+v", record.Promotion)
	}
	for i, result := range record.Promotion.Results {
		want := "ok"
		if i >= 6 {
			want = "budget_reached"
		}
		if result.ID != fmt.Sprintf("read-%d", i+1) || string(result.Code) != want {
			t.Fatalf("gathered result %d: %+v", i, result)
		}
	}
	running, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "running", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	if len(running.Welcome.Capabilities) != 1 {
		t.Fatal("installed Core does not advertise running")
	}
	topic := "running"
	if _, err = running.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := running.Receive(h.ctx)
	if err != nil || !strings.Contains(string(mustJSON(t, snapshot)), scenario.Input) {
		t.Fatal("running snapshot lost run", err)
	}
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 120, 60)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "\x12")
	waitScreen(t, h, terminal, "in_progress: "+scenario.Input)
	h.keys(terminal, "\x1b")
	key := "boundary-407"
	if _, err = client.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &scenario.Next}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-nextStarted:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	close(finishBackground)
	// A landed running row is an explicit completion barrier; no timing sleeps.
	for {
		frame, err := running.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(mustJSON(t, frame)), "ready_for_review") {
			break
		}
	}
	if err := db.QueryRowContext(h.ctx, "SELECT count(*) FROM lane_notice_events").Scan(&count); err != nil || count != 0 {
		t.Fatalf("notice delivered inside active turn: %d %v", count, err)
	}
	close(finishNext)
	ended, landed := false, false
	for !landed {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			ended = true
		}
		if frame.Kind == protocol.KindBackgroundNotice {
			if !ended {
				t.Fatal("landing interrupted active turn")
			}
			landed = true
		}
	}
	if err = db.QueryRowContext(h.ctx, "SELECT record FROM background_runs").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Outcome != runs.Complete || record.Artifact.Body != scenario.Artifact || record.Material.ID == "" || record.Material.Revision < 1 {
		t.Fatalf("result not durably landed: %+v", record)
	}
	waitScreen(t, h, terminal, scenario.Reply)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	client.Close()
	running.Close()
	db.Close()
	h.stopCore(core)
	core = start(true)
	again, conv := conversationClient(t, h)
	restoredTopic := "conversation:" + conv
	if _, err = again.Subscribe(h.ctx, protocol.Subscribe{Topic: &restoredTopic}); err != nil {
		t.Fatal(err)
	}
	restored, err := again.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, ok := restored.Payload.(protocol.Snapshot)
	if !ok {
		t.Fatal("missing restored snapshot")
	}
	notices := 0
	for _, item := range history.Items {
		if item.Kind == protocol.KindBackgroundNotice {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("restored landing count=%d", notices)
	}
	again.Close()
	if foreground.Load() != 10 || background.Load() != 1 {
		t.Fatalf("restart replayed provider: foreground=%d background=%d", foreground.Load(), background.Load())
	}
	t.Log("Hard: 10 foreground requests, one run, gathered evidence retained; running and Ctrl+R visible; landing after active turn; restart without replay")
}

func TestM1StartRunAtSoftInstalledCore(t *testing.T) {
	t.Parallel()

	var foreground, background atomic.Int32
	runStarted := make(chan struct{}, 1)
	const goal = "SOFT-RUN-407"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input, Tools []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			if background.Add(1) != 1 {
				t.Error("duplicate explicit run")
			}
			runStarted <- struct{}{}
			emitResponse(w, string(mustJSON(t, runs.Artifact{Body: "SOFT-RESULT-407", Done: true})), true)
			return
		}
		n := foreground.Add(1)
		switch {
		case n <= 6:
			emitPersonaCall(w, fmt.Sprintf("soft-read-%d", n), "skill_index", map[string]any{})
		case n == 7:
			offered := false
			for _, raw := range request.Tools {
				var decl struct{ Name string }
				if err := json.Unmarshal(raw, &decl); err != nil {
					t.Error(err)
				}
				offered = offered || decl.Name == "start_run"
			}
			if !offered {
				t.Error("start_run unavailable at Soft")
			}
			emitPersonaCall(w, "soft-start", "start_run", runs.Brief{Goal: goal, Scope: "read the skill index", NotTouch: "writes", Done: "complete inventory", Result: "summary", Allow: []string{"skill_index"}, Checks: []runs.Check{{Name: "complete inventory", Kind: runs.Claimed}}})
		case n == 8:
			found := false
			for _, raw := range request.Input {
				var item struct {
					Type   string
					CallID string `json:"call_id"`
					Output string
				}
				if err := json.Unmarshal(raw, &item); err != nil {
					t.Error(err)
				}
				if item.CallID == "soft-start" && item.Type == "function_call_output" {
					var result struct {
						Code   string
						Output runs.Receipt
					}
					if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
						t.Error(err)
					}
					if result.Code != "ok" || result.Output.ID == "" {
						t.Errorf("Soft start receipt: %s", item.Output)
					}
					found = true
				}
			}
			if !found {
				t.Error("missing committed start receipt")
			}
			emitResponse(w, "Work continues in the background.", true)
		default:
			t.Error("unexpected foreground replay")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer h.stopCore(core)
	client, conversation := conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "soft-start-407", goal)
	select {
	case <-runStarted:
	case <-h.ctx.Done():
		t.Fatal("explicit run was never released")
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM background_runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("runs=%d err=%v", count, err)
	}
	if foreground.Load() != 8 || background.Load() != 1 {
		t.Fatalf("requests foreground=%d background=%d", foreground.Load(), background.Load())
	}
}
