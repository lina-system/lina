// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM3PlanDrafting (m3/plan-drafting.json) exercises accepted goal discovery
// through model tools, Klotho drafting through a real Moirai worker and
// installed Core job socket, complete preview replacement, retirement of
// overlapping stale requests, stale confirmation refusal, one-version record
// creation, and idempotent confirmation across Core restarts. A loopback
// provider supplies only model output; all tool, job, result, protocol and
// records paths are production paths. No external credentials or services are
// used.
//
// Real installed Core, real Moirai process, real protocol and Git; only model
// output is scripted. No source-tree or test-only Core service composition.
func TestM3PlanDrafting(t *testing.T) {
	raw, err := os.ReadFile("../m3/plan-drafting.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Goal, Done           string
		Initial, Replacement jobapi.PlanDraft
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var root, id string
	var revision int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Instructions string
			Tools        []struct{ Name string }
			Input        []json.RawMessage
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad provider request")
			return
		}
		n := calls.Add(1)
		switch n {
		case 1:
			emitPersonaCall(w, "index", "plan_index", struct{}{})
		case 5:
			emitPersonaCall(w, "read", "read_plan", map[string]string{"result_id": id})
		case 2, 6:
			var item struct{ Output string }
			json.Unmarshal(req.Input[len(req.Input)-1], &item)
			var response struct {
				Code   string
				Output json.RawMessage
			}
			json.Unmarshal([]byte(item.Output), &response)
			if response.Code != "ok" {
				t.Errorf("planning read failed: %s", item.Output)
			}
			if n == 2 {
				var index struct{ Targets []struct{ ID string } }
				json.Unmarshal(response.Output, &index)
				if len(index.Targets) != 1 || index.Targets[0].ID != root {
					t.Error("accepted root not discoverable")
				}
			}
			if n == 6 {
				var plan struct{ Revision int }
				json.Unmarshal(response.Output, &plan)
				if plan.Revision != 1 {
					t.Error("cannot read current draft")
				}
			}

			offered := false
			for _, tool := range req.Tools {
				if tool.Name == "draft_plan" {
					offered = true
				}
				if strings.Contains(tool.Name, "confirm_plan") {
					t.Error("model has confirmation authority")
				}
			}
			if !offered {
				t.Error("Core did not offer draft_plan")
			}
			emitPersonaCall(w, "draft", "draft_plan", coreexec.PlanDraftRequest{RootID: root, ResultID: id, BaseRevision: revision, Instruction: "Draft the full plan"})
		case 3, 7, 8:
			var item struct{ Output string }
			if len(req.Input) == 0 {
				t.Error("no tool result")
				return
			}
			json.Unmarshal(req.Input[len(req.Input)-1], &item)
			var receipt struct {
				Code   string
				Output struct{ State string }
			}
			json.Unmarshal([]byte(item.Output), &receipt)
			if receipt.Code != "ok" || receipt.Output.State != "queued" {
				t.Errorf("draft queue failed: %s", item.Output)
			}
			if n == 7 {
				// Queue a second valid replacement before the worker applies the first.
				emitPersonaCall(w, "draft-overlap", "draft_plan", coreexec.PlanDraftRequest{RootID: root, ResultID: id, BaseRevision: revision, Instruction: "Replace the complete draft"})
			} else {
				emitResponse(w, "Preparing your plan.", true)
			}
		case 4, 9, 10:
			if len(req.Tools) != 0 || req.Instructions != "" || len(req.Input) != 2 {
				t.Error("planning did not use auxiliary tool-free route")
			}
			if len(req.Input) == 2 {
				var instruction, source struct{ Role, Content string }
				json.Unmarshal(req.Input[0], &instruction)
				json.Unmarshal(req.Input[1], &source)
				var input jobapi.PlanInput
				json.Unmarshal([]byte(source.Content), &input)
				if instruction.Role != "developer" || instruction.Content == "" || source.Role != "user" || input.RootID != root || input.Kind != "goal" {
					t.Error("planning request lost methodology or accepted root")
				}
			}
			draft := scenario.Initial
			if n != 4 {
				draft = scenario.Replacement
			}
			out, _ := json.Marshal(draft)
			emitResponse(w, string(out), true)
		default:
			t.Errorf("unexpected model call %d", n)
			emitResponse(w, "Unexpected", true)
		}
	}))
	defer server.Close()
	workerBinary := buildCommand(t, "moirai-worker")
	h, _ := installedConversationHarness(t, server.URL)
	// This scenario owns three cold Core starts, three worker processes and ten
	// model exchanges. Each process retains its own bounded readiness/exit wait.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connectWithin(core, 30*time.Second).Close()
		waitCoreReady(t, h)
		return core
	}
	core := start()
	conv, conversation := conversationClient(t, h)
	connect := func() *clientapi.Client {
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "planning", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	client := connect()
	agent := client.Welcome.Agents[0].AgentID
	request := func(p protocol.PlanRequest) (protocol.PlanResponse, *protocol.Error) {
		t.Helper()
		p.AgentID = agent
		if _, err := client.Plan(h.ctx, p); err != nil {
			t.Fatal(err)
		}
		e, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.Error != nil {
			return protocol.PlanResponse{}, e.Error
		}
		response, ok := e.Payload.(protocol.PlanResponse)
		if !ok {
			t.Fatalf("unexpected plan response: %+v", e)
		}
		return response, nil
	}
	accepted, failure := request(protocol.PlanRequest{Operation: "accept_goal", Title: &scenario.Goal, Done: &scenario.Done})
	if failure != nil || accepted.RecordID == nil {
		t.Fatal(accepted, failure)
	}
	root = *accepted.RecordID
	repo := filepath.Join(h.root, "versions/records")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(h.ctx, "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	before := git("rev-parse", "HEAD")
	worker := func() {
		t.Helper()
		child := *h
		child.binary = workerBinary
		p := child.command("--canon", filepath.Join(h.root, "canon/canon.sqlite"), "--socket", filepath.Join(h.root, "run/jobs.sock"), "--once")
		p.start(t)
		p.waitWithin(t, 0, 30*time.Second)
	}
	nextPlan := func() protocol.Result {
		t.Helper()
		for {
			e, err := conv.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if p, ok := e.Payload.(protocol.Result); ok && p.Type == protocol.ResultTypePlanView {
				return p
			}
		}
	}
	admittedTurn(t, h, conv, conversation, "plan-first", "Plan the accepted goal")
	worker()
	first := nextPlan()
	id = first.ResultID
	revision = 1
	if first.Revision != "1" || first.Body == nil {
		t.Fatal("missing first plan", first)
	}
	if git("rev-parse", "HEAD") != before {
		t.Fatal("draft created plan objects")
	}
	admittedTurn(t, h, conv, conversation, "plan-replace", "Replace the draft with a better plan")
	worker()
	second := nextPlan()
	if second.ResultID != id || second.Revision != "2" {
		t.Fatal("replacement identity", second)
	}
	var body protocol.PlanBody
	if json.Unmarshal(*second.Body, &body) != nil || len(body.Entries) != 3 || body.Entries[2].Title != "Book the room" || body.Confirm == nil {
		t.Fatal("replacement retained stale entries", body)
	}
	if git("rev-parse", "HEAD") != before {
		t.Fatal("replacement created plan objects")
	}
	// The obsolete second replacement must finish, publish its outcome and leave
	// revision 2 intact instead of pinning the worker at an unconsumable task.
	worker()
	for {
		event, err := conv.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := event.Payload.(protocol.ItemDone); ok && item.Stream != nil && strings.HasPrefix(item.Stream.Key, "plan-rejection:") {
			if item.Speaker == nil || *item.Speaker != protocol.ConversationSpeakerSystem || item.Text == nil || *item.Text == "" {
				t.Fatal("missing rejection outcome", item)
			}
			break
		}
	}
	unchanged, failure := request(protocol.PlanRequest{Operation: "get", ResultID: &id})
	if failure != nil || unchanged.Result == nil || unchanged.Result.Revision != "2" {
		t.Fatal("rejection changed current plan", unchanged, failure)
	}
	old := json.Number("1")
	if _, failure = request(protocol.PlanRequest{Operation: "confirm", ResultID: &id, Revision: &old}); failure == nil || failure.Code != protocol.ErrorStaleRevision {
		t.Fatal("stale confirmation", failure)
	}
	// Restart with the unconfirmed preview; no model replay and no record creation.
	conv.Close()
	client.Close()
	h.stopCore(core)
	core = start()
	conv, conversation = conversationClient(t, h)
	client = connect()
	restored, failure := request(protocol.PlanRequest{Operation: "get", ResultID: &id})
	if failure != nil || restored.Result == nil || restored.Result.Revision != "2" {
		t.Fatal(restored, failure)
	}
	current := json.Number("2")
	confirmed, failure := request(protocol.PlanRequest{Operation: "confirm", ResultID: &id, Revision: &current})
	if failure != nil || confirmed.EffectState == nil || *confirmed.EffectState != protocol.EffectApplied || confirmed.Records == nil || len(*confirmed.Records) != 2 {
		t.Fatal(confirmed, failure)
	}
	if git("rev-list", "--count", "HEAD") != "2" {
		t.Fatal("confirmation was not one saved version")
	}
	for _, name := range strings.Split(git("ls-tree", "-r", "--name-only", "HEAD"), "\n") {
		contents := git("show", "HEAD:"+name)
		if strings.Contains(contents, "Discarded venue search") {
			t.Fatal("deleted draft entry persisted")
		}
	}
	head := git("rev-parse", "HEAD")
	conv.Close()
	client.Close()
	h.stopCore(core)
	core = start()
	conv, conversation = conversationClient(t, h)
	client = connect()
	again, failure := request(protocol.PlanRequest{Operation: "confirm", ResultID: &id, Revision: &current})
	if failure != nil || again.Revision == nil || *again.Revision != head || git("rev-parse", "HEAD") != head {
		t.Fatal("repeat confirmation changed records", again, failure)
	}
	if calls.Load() != 10 {
		t.Fatal("replayed planning on restart", calls.Load())
	}
	if git("status", "--porcelain") != "" {
		t.Fatal("records not clean")
	}
	git("fsck", "--strict")
	conv.Close()
	client.Close()
	h.stopCore(core)
}
