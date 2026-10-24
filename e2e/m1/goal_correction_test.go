// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

// goalReply is what the fixture answers once an operation's tool receipt
// arrives. A turn that ends before this reply never showed the person its
// outcome, even when the goal state it changed is correct.
const goalReply = "Goal operation checked."

// goalLines are the person's lines in the order the scenario sends them. The
// request that carries the next line starts its operation; anything else on
// this route — compaction's summary, background extraction — is answered
// without advancing the scenario, so it cannot shift a later turn's answer.
var goalLines = []string{
	"Update the library.",
	"Limit that work to the parser.",
	"Check whether the earlier revision is still current.",
	"Read the corrected goal.",
}

// goalScript follows the conversation's turns instead of counting provider
// requests: a person's line starts the next operation, and the tool output that
// follows continues the same one.
type goalScript struct {
	mu            sync.Mutex
	operations    int
	continuations int
	other         int
	admitted      work.DefinitionReceipt
	corrected     work.DefinitionReceipt
}

// scenarioRequest says how a request on this route relates to the scenario.
type scenarioRequest int

const (
	unrelatedRequest scenarioRequest = iota
	operationRequest
	continuationRequest
)

// carries reports whether any input item holds line. A turn repeats earlier
// lines from history, so the caller asks only about the next one.
func carries(items []json.RawMessage, line string) bool {
	for _, item := range items {
		if bytes.Contains(item, []byte(line)) {
			return true
		}
	}
	return false
}

// classify places a request on this route. A committed tool output continues
// the operation that produced it; the next person line, which no earlier turn
// carried, starts the following one; every other request — a summary, a
// background extraction — belongs to other work and must not advance the
// scenario. next is the operation the scenario expects to start.
func classify(items []json.RawMessage, next int) (scenarioRequest, int) {
	if len(items) == 0 {
		return unrelatedRequest, next
	}
	var last struct{ Type string }
	_ = json.Unmarshal(items[len(items)-1], &last)
	if last.Type == "function_call_output" || last.Type == "custom_tool_call_output" {
		return continuationRequest, next - 1
	}
	if next < len(goalLines) && carries(items, goalLines[next]) {
		return operationRequest, next
	}
	return unrelatedRequest, next
}

func (s *goalScript) status() (operations, continuations, other int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.operations, s.continuations, s.other
}

type correctionDriver struct{}

func (correctionDriver) Authorize(context.Context, work.Action) error { return work.ErrNeedsPerson }
func (correctionDriver) Execute(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, work.ErrInvalid
}
func (correctionDriver) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, work.ErrInvalid
}

// A request for other work on Core's route must not consume the scenario's
// next answer: a shifted script fails an unrelated turn and hides the real one.
func TestGoalCorrectionScriptIgnoresOtherRequests(t *testing.T) {
	person := func(line string) json.RawMessage {
		body, err := json.Marshal(map[string]any{"role": "developer", "content": []map[string]string{{"type": "input_text", "text": line}}})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	output := json.RawMessage(`{"type":"function_call_output","call_id":"call","output":"{}"}`)
	summary := person(`Summarize this segment: {"unanswered_requests":"","omitted_detail":""}`)
	for _, c := range []struct {
		name  string
		items []json.RawMessage
		next  int
		kind  scenarioRequest
		index int
	}{
		{"the next person line starts its operation", []json.RawMessage{person("Earlier line."), person(goalLines[2])}, 2, operationRequest, 2},
		{"a committed tool output continues its operation", []json.RawMessage{person(goalLines[2]), output}, 3, continuationRequest, 2},
		{"a summary belongs to other work", []json.RawMessage{person(goalLines[1]), summary}, 2, unrelatedRequest, 2},
		{"history alone does not restart a finished operation", []json.RawMessage{person(goalLines[1]), summary}, 3, unrelatedRequest, 3},
		{"no operation remains to start", []json.RawMessage{person(goalLines[3])}, 4, unrelatedRequest, 4},
		{"an empty request belongs to no operation", nil, 1, unrelatedRequest, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			kind, index := classify(c.items, c.next)
			if kind != c.kind || index != c.index {
				t.Fatalf("classify=%d,%d want %d,%d", kind, index, c.kind, c.index)
			}
		})
	}
}

func TestM2ConversationCorrectsAdmittedGoal(t *testing.T) {
	t.Parallel()
	var script goalScript
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage
			Tools []fixtureToolDeclaration
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		// One request at a time: the serial conversation loop owns the receipts
		// below, and unrelated work may reuse this route.
		script.mu.Lock()
		defer script.mu.Unlock()
		kind, n := classify(request.Input, script.operations)
		if kind == unrelatedRequest {
			// Answer so the request completes without changing the state the
			// next turn expects.
			script.other++
			emitResponse(w, "Unrelated request.", true)
			return
		}
		if kind == operationRequest {
			script.operations++
			tool := "dispatch_code_goal"
			args := map[string]any{
				"outcome": "update the library", "done": "library checks pass",
				"scope":    work.GoalScope{Repositories: []string{"fixture"}, Includes: "library"},
				"criteria": []work.Criterion{{ID: "checks", Statement: "checks pass", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "test", Target: "fixture"}}},
			}
			switch n {
			case 0:
				args["tasks"], args["no_service_reason"] = []any{}, "library"
			case 1, 2:
				tool = "revise_code_goal"
				args["goal_id"], args["revision"] = script.admitted.GoalID, script.admitted.Revision
				args["outcome"] = "update only the parser"
				args["scope"] = work.GoalScope{Repositories: []string{"fixture"}, Includes: "parser", Excludes: "service"}
			case 3:
				tool, args = "read_record", map[string]any{"id": script.corrected.RecordID}
			default:
				t.Errorf("conversation started an unexpected operation %d", n)
				emitResponse(w, "Unexpected request.", true)
				return
			}
			if !toolReachable(request.Tools, tool) {
				t.Errorf("conversation did not declare %s", tool)
			}
			emitPersonaCall(w, "goal-operation", tool, args)
			return
		}
		script.continuations++
		if n < 0 {
			t.Error("conversation continued an operation it never started")
			return
		}
		var item struct{ Type, Output string }
		var result struct {
			Code   string
			Output json.RawMessage
		}
		if err := json.Unmarshal(request.Input[len(request.Input)-1], &item); err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
			t.Error(err)
		}
		if n != 2 && result.Code != "ok" {
			t.Errorf("goal tool failed: %s", item.Output)
		}
		switch n {
		case 0:
			if err := json.Unmarshal(result.Output, &script.admitted); err != nil || script.admitted.GoalID == "" || script.admitted.RecordID == "" || script.admitted.Revision == "" {
				t.Errorf("missing admission receipt: %s", result.Output)
			}
		case 1:
			if err := json.Unmarshal(result.Output, &script.corrected); err != nil || script.corrected.GoalID != script.admitted.GoalID || script.corrected.RecordID != script.admitted.RecordID || script.corrected.Revision == script.admitted.Revision {
				t.Errorf("correction replaced the goal or lost its revision: %s", result.Output)
			}
		case 2:
			if result.Code == "ok" || !strings.Contains(string(result.Output), "stale_revision") {
				t.Errorf("stale conversation correction was accepted: %s", item.Output)
			}
		case 3:
			var record coreexec.RecordReceipt
			if err := json.Unmarshal(result.Output, &record); err != nil || record.ID != script.corrected.RecordID || record.Revision != script.corrected.Revision || !strings.Contains(record.Markdown, "update only the parser") {
				t.Errorf("restart lost the corrected canonical record: %s", result.Output)
			}
		default:
			t.Errorf("conversation continued an unexpected operation %d", n)
		}
		emitResponse(w, goalReply, true)
	})))
	defer server.Close()
	h := newHarness(t, "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Core runs in this process. Its build attestation must describe this
	// executable, as in the other embedded-Core conversation scenarios.
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"system/bin", "system/core"} {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), provenance, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"goal-correction-host"}`), 0600); err != nil {
		t.Fatal(err)
	}
	route, err := modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": server.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	var stop func()
	t.Cleanup(func() {
		if stop != nil {
			stop()
		}
	})
	start := func() *work.Engine {
		ctx, cancel := context.WithCancel(h.ctx)
		core := &process{done: make(chan struct{})}
		ready := make(chan *work.Engine, 1)
		go func() {
			core.err = boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route, Work: &boot.WorkConfig{
				Driver: func(e *work.Engine) (work.Driver, error) { ready <- e; return correctionDriver{}, nil },
			}})
			close(core.done)
		}()
		stop = func() {
			cancel()
			select {
			case <-core.done:
				if core.err != nil {
					t.Error(core.err)
				}
			case <-time.After(operationTimeout):
				t.Error("Core shutdown timed out")
			}
		}
		h.connect(core).Close()
		waitCoreReady(t, h)
		return <-ready
	}
	engine := start()
	c, conversation := conversationClient(t, h)
	// The reply is the operation's visible outcome. Checking it here fails a
	// turn that ended after its tool call, which the goal snapshot alone cannot
	// show: the receipt is already committed. A failure notice names the cause.
	turn := func(key, text string) {
		t.Helper()
		replied := false
		var notices []string
		for _, frame := range admittedTurn(t, h, c, conversation, key, text) {
			item, ok := frame.Payload.(protocol.ItemDone)
			if !ok || item.Text == nil {
				continue
			}
			switch {
			case *item.Text == goalReply:
				replied = true
			case item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem:
				notices = append(notices, *item.Text)
			}
		}
		if !replied {
			operations, continuations, _ := script.status()
			t.Fatalf("%s ended without showing its outcome after %d of %d operations answered: %v", key, continuations, operations, notices)
		}
	}
	turn("admit-goal", "Update the library.")
	turn("correct-goal", "Limit that work to the parser.")
	turn("stale-goal", "Check whether the earlier revision is still current.")
	before, err := engine.Snapshot(h.ctx)
	if err != nil || len(before.Goals) != 1 || before.Goals[0].Summary != "update only the parser" || !before.Goals[0].DefinitionPending {
		t.Fatalf("conversation did not revise the running goal: %+v %v", before, err)
	}
	c.Close()
	stop()
	engine = start()
	c, conversation = conversationClient(t, h)
	turn("read-corrected-goal", "Read the corrected goal.")
	after, err := engine.Snapshot(h.ctx)
	if err != nil || len(after.Goals) != 1 || after.Goals[0].ID != before.Goals[0].ID || after.Goals[0].RecordRevision != before.Goals[0].RecordRevision || after.Goals[0].Scope.Includes != "parser" {
		t.Fatalf("restart changed the admitted goal: %+v %v", after, err)
	}
	c.Close()
	stop()
	stop = nil
	if operations, continuations, other := script.status(); operations != len(goalLines) || continuations != len(goalLines) {
		t.Fatalf("conversation did not complete all four operations: %d started, %d answered, %d unrelated", operations, continuations, other)
	}
}
