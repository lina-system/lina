// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	readcanon "github.com/thisisjun786/lina/internal/moirai/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Completed malformed responses must survive a Core restart between Aux and
// Apply. All services, workers and storage are real; only model output is scripted.
func TestM3InvalidPlanningResponsesReleaseQueue(t *testing.T) {
	raw, err := os.ReadFile("../m3/plan-invalid-response.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Goal, Done string
		Invalid    []string
		Valid      json.RawMessage
		Oversized  int
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	scenario.Invalid = append(scenario.Invalid, strings.Repeat("x", scenario.Oversized))
	var calls atomic.Int64
	var root string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []json.RawMessage
			Input []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Tools) == 0 {
			n := int(calls.Add(1)) - 1
			if n > len(scenario.Invalid) {
				// A disconnected, incomplete call must remain unknown, not be
				// mistaken for a completed invalid result.
				emitResponse(w, "{", false)
				return
			}
			text := string(scenario.Valid)
			if n < len(scenario.Invalid) {
				text = scenario.Invalid[n]
			}
			emitResponse(w, text, false)
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
			return
		}
		var last struct{ Type, Output string }
		if len(request.Input) != 0 {
			json.Unmarshal(request.Input[len(request.Input)-1], &last)
		}
		if last.Type == "function_call_output" {
			var receipt struct{ Code string }
			if json.Unmarshal([]byte(last.Output), &receipt) != nil || receipt.Code != "ok" {
				t.Errorf("planning queue failed: %s", last.Output)
			}
			emitResponse(w, "Preparing the plan.", true)
			return
		}
		emitPersonaCall(w, "draft", "draft_plan", coreexec.PlanDraftRequest{RootID: root, Instruction: "Draft the full plan"})
	}))
	defer server.Close()
	workerBinary := buildCommand(t, "moirai-worker")
	h, start := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	core := start(true)
	conv, conversation := conversationClient(t, h)
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "planning", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	agent := client.Welcome.Agents[0].AgentID
	if _, err = client.Plan(h.ctx, protocol.PlanRequest{AgentID: agent, Operation: "accept_goal", Title: &scenario.Goal, Done: &scenario.Done}); err != nil {
		t.Fatal(err)
	}
	event, err := client.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	accepted, ok := event.Payload.(protocol.PlanResponse)
	if !ok || accepted.RecordID == nil {
		t.Fatalf("acceptance: %+v", event)
	}
	root = *accepted.RecordID
	client.Close()
	jobID := "planning:" + agent
	api := jobapi.NewClient(filepath.Join(h.root, "run/jobs.sock"))
	path := filepath.Join(h.root, "canon/canon.sqlite")
	db, err := canon.OpenSQLiteReader(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	count := func(query string, want int64) {
		t.Helper()
		var got int64
		if err := db.QueryRowContext(h.ctx, query, jobID).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d want %d: %v", query, got, want, err)
		}
	}
	ready := func(sequence int64) jobapi.Work {
		t.Helper()
		reader, err := readcanon.Open(h.ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		work, err := reader.Ready(h.ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range work {
			if item.Job.ID == jobID && item.Task.Sequence == sequence {
				return item
			}
		}
		t.Fatalf("queue did not advance to %d: %+v", sequence, work)
		return jobapi.Work{}
	}
	restart := func() {
		t.Helper()
		conv.Close()
		h.stopCore(core)
		core = start(true)
		conv, conversation = conversationClient(t, h)
	}
	worker := func() {
		t.Helper()
		child := *h
		child.binary = workerBinary
		p := child.command("--canon", path, "--socket", filepath.Join(h.root, "run/jobs.sock"), "--once")
		p.start(t)
		p.waitWithin(t, 0, 30*time.Second)
	}
	// The valid request is already waiting behind the invalid ones.
	for i := 0; i <= len(scenario.Invalid); i++ {
		admittedTurn(t, h, conv, conversation, fmt.Sprintf("plan-invalid-%d", i), "Plan the accepted goal")
	}
	for i := range scenario.Invalid {
		sequence := int64(i + 1)
		work := ready(sequence)
		aux, err := api.Aux(h.ctx, jobapi.AuxRequest{Epoch: work.Epoch, JobID: jobID, From: work.Job.Watermark, Through: sequence})
		if err != nil {
			t.Fatalf("completed invalid response stranded Aux: %v", err)
		}
		count("SELECT count(*) FROM moirai_aux WHERE job=? AND output IS NULL", 0)
		count("SELECT count(*) FROM budget_usage WHERE run=? AND settled=1", sequence)
		count("SELECT sum(charged) FROM budget_usage WHERE run=?", sequence*10)
		// Restart after the completed response is stored but before adoption.
		restart()
		work = ready(sequence)
		worker()
		if calls.Load() != sequence {
			t.Fatal("completed invalid call replayed after restart", calls.Load())
		}
		key := fmt.Sprintf("plan-rejection:%s:%d", jobID, sequence)
		for {
			event, err := conv.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if item, ok := event.Payload.(protocol.ItemDone); ok && item.Stream != nil && item.Stream.Key == key {
				if item.Text == nil || *item.Text == "" || item.Speaker == nil || *item.Speaker != protocol.ConversationSpeakerSystem {
					t.Fatal("missing rejection outcome", item)
				}
				break
			}
		}
		// An identical proposal only returns its receipt; no second rejection.
		proposal := jobapi.Proposal{Epoch: work.Epoch, JobID: jobID, From: work.Job.Watermark, Through: sequence, Output: aux.Output}
		if applied, err := api.Apply(h.ctx, proposal); err != nil || applied.Watermark != sequence {
			t.Fatal("repeated rejection failed", applied, err)
		}
		count("SELECT count(*) FROM plan_rejections WHERE job=? AND code='invalid_plan'", sequence)
		count("SELECT count(*) FROM moirai_applied WHERE job=?", sequence)
		count("SELECT count(*) FROM conv_event WHERE scaffold_kind='plan_rejection' AND id LIKE 'plan-rejection:' || ? || ':%'", sequence)
		count("SELECT count(*) FROM result_heads WHERE ? IS NOT NULL", 0)
		count("SELECT count(*) FROM records_index WHERE ? IS NOT NULL", 1)
	}
	validSequence := int64(len(scenario.Invalid) + 1)
	validWork := ready(validSequence)
	if _, err := api.Aux(h.ctx, jobapi.AuxRequest{Epoch: validWork.Epoch, JobID: jobID, From: validWork.Job.Watermark, Through: validSequence}); err != nil {
		t.Fatal(err)
	}
	// A worker cannot replace a completed valid response with its own rejection.
	if _, err := api.Apply(h.ctx, jobapi.Proposal{Epoch: validWork.Epoch, JobID: jobID, From: validWork.Job.Watermark, Through: validSequence, Output: json.RawMessage(jobapi.InvalidPlanResult)}); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatal("forged rejection accepted", err)
	}
	worker()
	for {
		event, err := conv.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if result, ok := event.Payload.(protocol.Result); ok && result.Type == protocol.ResultTypePlanView {
			if result.Revision != "1" || result.Body == nil {
				t.Fatal("missing subsequent valid plan", result)
			}
			break
		}
	}
	count("SELECT count(*) FROM moirai_applied WHERE job=?", validSequence)
	count("SELECT sum(charged) FROM budget_usage WHERE run=?", validSequence*10)
	count("SELECT count(*) FROM records_index WHERE ? IS NOT NULL", 1)
	restart()
	if calls.Load() != validSequence {
		t.Fatal("restart replayed completed calls", calls.Load())
	}
	topic := "conversation:" + conversation
	request, err := conv.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
	if err != nil {
		t.Fatal(err)
	}
	for {
		event, err := conv.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if event.ReplyTo == nil || *event.ReplyTo != request {
			continue
		}
		history, ok := event.Payload.(protocol.Snapshot)
		if !ok || history.Topic != topic {
			t.Fatal("missing restored history", event)
		}
		seen := map[string]bool{}
		for _, item := range history.Items {
			if strings.HasPrefix(item.Key, "plan-rejection:") {
				if seen[item.Key] {
					t.Fatal("duplicate rejection notice", item.Key)
				}
				seen[item.Key] = true
			}
		}
		if len(seen) != len(scenario.Invalid) {
			t.Fatal("rejection notices were not restored", seen)
		}
		break
	}
	// Incomplete streams retain unknown ownership across restart and are never
	// silently retried or counted as a completed rejection.
	admittedTurn(t, h, conv, conversation, "plan-unknown", "Plan again")
	work := ready(validSequence + 1)
	if _, err := api.Aux(h.ctx, jobapi.AuxRequest{Epoch: work.Epoch, JobID: jobID, From: work.Job.Watermark, Through: work.Task.Sequence}); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatal("incomplete call was treated as completed", err)
	}
	restart()
	work = ready(validSequence + 1)
	if _, err := api.Aux(h.ctx, jobapi.AuxRequest{Epoch: work.Epoch, JobID: jobID, From: work.Job.Watermark, Through: work.Task.Sequence}); !errors.Is(err, jobapi.ErrPending) {
		t.Fatal("unknown call lost its reservation", err)
	}
	if calls.Load() != validSequence+1 {
		t.Fatal("unknown call replayed", calls.Load())
	}
	count("SELECT count(*) FROM moirai_aux WHERE job=? AND output IS NULL", 1)
	count("SELECT count(*) FROM budget_usage WHERE run=? AND settled=1", validSequence)
	count("SELECT count(*) FROM plan_rejections WHERE job=?", int64(len(scenario.Invalid)))
	count("SELECT json_extract(record, '$.Watermark') FROM moirai_jobs WHERE id=?", validSequence)
	conv.Close()
	h.stopCore(core)
}
