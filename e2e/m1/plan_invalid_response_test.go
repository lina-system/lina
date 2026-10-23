// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM3PlanInvalidResponseReleasesQueue (m3/plan-invalid-response.json)
// exercises completed invalid JSON and cyclic planning responses through
// installed Core and Moirai. It restarts Core between Aux completion and Apply,
// verifies cached results, rejects a forged proposal, checks durable rejection
// and usage counts under duplicate application, and observes the next valid
// preview without publishing unconfirmed plan records.
func TestM3PlanInvalidResponseReleasesQueue(t *testing.T) {
	runPlanInvalidResponseScenario(t, "../m3/plan-invalid-response.json")
}

func runPlanInvalidResponseScenario(t *testing.T, fixture string) {
	t.Helper()
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Kind, Goal, Done string
		Invalid          []string
		Valid            jobapi.PlanDraft
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var root string
	var turns, auxiliary atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Tools, Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if answerEmptyMemoryExtraction(w, req.Input) {
			return
		}
		if len(req.Tools) != 0 {
			if turns.Add(1)%2 == 1 {
				emitPersonaCall(w, "draft", "draft_plan", coreexec.PlanDraftRequest{RootID: root, Instruction: "Plan the guide"})
			} else {
				emitResponse(w, "Preparing the plan.", true)
			}
			return
		}
		n := int(auxiliary.Add(1)) - 1
		if n < len(scenario.Invalid) {
			emitResponse(w, scenario.Invalid[n], true)
			return
		}
		if n != len(scenario.Invalid) {
			t.Errorf("repeated model call %d", n)
		}
		out, _ := json.Marshal(scenario.Valid)
		emitResponse(w, string(out), true)
	}))
	defer server.Close()
	workerBinary := buildCommand(t, "moirai-worker")
	h, _ := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	start := func() *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connectWithin(p, 30*time.Second).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	conv, conversation := conversationClient(t, h)
	client, err := clientapi.DialWithCapabilities(ctx, h.socket, "test", []protocol.Capability{{Name: "planning", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	agent := client.Welcome.Agents[0].AgentID
	if _, err = client.Plan(ctx, protocol.PlanRequest{AgentID: agent, Operation: "accept_" + scenario.Kind, Title: &scenario.Goal, Done: &scenario.Done}); err != nil {
		t.Fatal(err)
	}
	e, err := client.Receive(ctx)
	if err != nil || e.Error != nil {
		t.Fatal(e, err)
	}
	accepted := e.Payload.(protocol.PlanResponse)
	root = *accepted.RecordID
	for n := 0; n <= len(scenario.Invalid); n++ {
		admittedTurn(t, h, conv, conversation, "invalid-plan-"+strconv.Itoa(n), "Prepare the guide plan")
	}
	client.Close()
	db, err := canon.OpenSQLiteReader(ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	epoch := func() uint64 {
		var value uint64
		if err := db.QueryRowContext(ctx, "SELECT epoch FROM canon_meta").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	api := jobapi.NewClient(filepath.Join(h.root, "run/jobs.sock"))
	job := "planning:" + agent
	request := jobapi.AuxRequest{Epoch: epoch(), JobID: job, Through: 1}
	first, err := api.Aux(ctx, request)
	if err != nil {
		t.Fatalf("completed invalid response must be retained for rejection: %v", err)
	}
	// Restart after the invalid call completed, before its proposal was applied.
	conv.Close()
	h.stopCore(core)
	core = start()
	conv, conversation = conversationClient(t, h)
	request.Epoch = epoch()
	again, err := api.Aux(ctx, request)
	if err != nil || string(again.Output) != string(first.Output) || auxiliary.Load() != 1 {
		t.Fatalf("completed invalid call replayed: %s %v calls=%d", again.Output, err, auxiliary.Load())
	}
	forged := jobapi.Proposal{Epoch: request.Epoch, JobID: job, Through: 1, Output: json.RawMessage(`{"title":"forged","summary":"forged","entries":[]}`)}
	if _, err = api.Apply(ctx, forged); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("forged rejection accepted: %v", err)
	}
	worker := func() {
		child := *h
		child.binary = workerBinary
		p := child.command("--canon", filepath.Join(h.root, "canon/canon.sqlite"), "--socket", filepath.Join(h.root, "run/jobs.sock"), "--once")
		p.start(t)
		p.waitWithin(t, 0, 30*time.Second)
	}
	for n := range scenario.Invalid {
		worker()
		for {
			e, err := conv.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if card, ok := e.Payload.(protocol.Result); ok && card.Type == protocol.ResultTypePlanView {
				t.Fatalf("invalid draft %d published a preview: %+v", n, card)
			}
			if item, ok := e.Payload.(protocol.ItemDone); ok && item.Stream != nil && item.Stream.Key == "plan-rejection:"+job+":"+strconv.Itoa(n+1) {
				if item.Text == nil || *item.Text == "" {
					t.Fatal("missing rejection outcome")
				}
				break
			}
		}
	}
	// Replaying the same proposal must not duplicate its receipt or notice.
	proposal := jobapi.Proposal{Epoch: request.Epoch, JobID: job, Through: 1, Output: first.Output}
	if _, err = api.Apply(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	worker()
	var preview protocol.Result
	for {
		e, err := conv.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if card, ok := e.Payload.(protocol.Result); ok && card.Type == protocol.ResultTypePlanView {
			if card.Title != scenario.Valid.Title || card.Revision != "1" {
				t.Fatal(card)
			}
			preview = card
			break
		}
	}
	for query, want := range map[string]int{
		"SELECT count(*) FROM plan_rejections":                                           len(scenario.Invalid),
		"SELECT count(*) FROM conv_event WHERE scaffold_kind='plan_rejection'":           len(scenario.Invalid),
		"SELECT count(*) FROM result_heads":                                              1,
		"SELECT count(*) FROM plan_confirmations":                                        0,
		"SELECT count(*) FROM moirai_aux WHERE job='" + job + "' AND output IS NOT NULL": len(scenario.Invalid) + 1,
		"SELECT count(*) FROM moirai_applied WHERE job='" + job + "'":                    len(scenario.Invalid) + 1,
		"SELECT count(*) FROM budget_usage WHERE run='" + job + "' AND settled=1":        len(scenario.Invalid) + 1,
		"SELECT sum(charged) FROM budget_usage WHERE run='" + job + "'":                  8192 * (len(scenario.Invalid) + 1),
	} {
		var got int
		if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %d, want %d: %v", query, got, want, err)
		}
	}
	var record []byte
	if err = db.QueryRowContext(ctx, "SELECT record FROM moirai_jobs WHERE id=?", job).Scan(&record); err != nil {
		t.Fatal(err)
	}
	var state jobapi.Job
	if json.Unmarshal(record, &state) != nil || state.Watermark != int64(len(scenario.Invalid)+1) {
		t.Fatal("queue watermark", string(record))
	}
	files, err := filepath.Glob(filepath.Join(h.root, "versions/records/projects/*.md"))
	if err != nil || len(files) != 1 {
		t.Fatal("draft created records", files, err)
	}
	if auxiliary.Load() != int64(len(scenario.Invalid)+1) {
		t.Fatal("model repeated", auxiliary.Load())
	}
	// The next valid draft must also survive the record store's dependency checks.
	client, err = clientapi.DialWithCapabilities(ctx, h.socket, "test", []protocol.Capability{{Name: "planning", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	revision := json.Number(preview.Revision)
	if _, err = client.Plan(ctx, protocol.PlanRequest{AgentID: agent, Operation: "confirm", ResultID: &preview.ResultID, Revision: &revision}); err != nil {
		t.Fatal(err)
	}
	e, err = client.Receive(ctx)
	if err != nil || e.Error != nil {
		t.Fatal("valid draft confirmation failed", e, err)
	}
	confirmed, ok := e.Payload.(protocol.PlanResponse)
	if !ok || confirmed.EffectState == nil || *confirmed.EffectState != protocol.EffectApplied || confirmed.Records == nil || len(*confirmed.Records) != len(scenario.Valid.Entries) {
		t.Fatal("valid draft did not create its records", confirmed)
	}
	client.Close()
	conv.Close()
	h.stopCore(core)
}
