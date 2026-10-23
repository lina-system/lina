// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

type correctionDriver struct{}

func (correctionDriver) Authorize(context.Context, work.Action) error { return work.ErrNeedsPerson }
func (correctionDriver) Execute(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, work.ErrInvalid
}
func (correctionDriver) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, work.ErrInvalid
}

func TestM2ConversationCorrectsAdmittedGoal(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	// Only the serial provider loop accesses these receipts.
	var admitted, corrected work.DefinitionReceipt
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage
			Tools []fixtureToolDeclaration
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		if n%2 == 1 {
			tool := "dispatch_code_goal"
			args := map[string]any{
				"outcome": "update the library", "done": "library checks pass",
				"scope":    work.GoalScope{Repositories: []string{"fixture"}, Includes: "library"},
				"criteria": []work.Criterion{{ID: "checks", Statement: "checks pass", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "test", Target: "fixture"}}},
			}
			switch n {
			case 1:
				args["tasks"], args["no_service_reason"] = []any{}, "library"
			case 3, 5:
				tool = "revise_code_goal"
				args["goal_id"], args["revision"] = admitted.GoalID, admitted.Revision
				args["outcome"] = "update only the parser"
				args["scope"] = work.GoalScope{Repositories: []string{"fixture"}, Includes: "parser", Excludes: "service"}
			case 7:
				tool, args = "read_record", map[string]any{"id": corrected.RecordID}
			default:
				t.Errorf("unexpected provider request %d", n)
				emitResponse(w, "Unexpected request.", true)
				return
			}
			found := toolReachable(request.Tools, tool)
			if !found {
				t.Errorf("conversation did not declare %s", tool)
			}
			emitPersonaCall(w, "goal-operation", tool, args)
			return
		}
		if len(request.Input) == 0 {
			t.Error("missing tool output")
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
		if item.Type != "function_call_output" || n != 6 && result.Code != "ok" {
			t.Errorf("goal tool failed: %s", item.Output)
		}
		switch n {
		case 2:
			if err := json.Unmarshal(result.Output, &admitted); err != nil || admitted.GoalID == "" || admitted.RecordID == "" || admitted.Revision == "" {
				t.Errorf("missing admission receipt: %s", result.Output)
			}
		case 4:
			if err := json.Unmarshal(result.Output, &corrected); err != nil || corrected.GoalID != admitted.GoalID || corrected.RecordID != admitted.RecordID || corrected.Revision == admitted.Revision {
				t.Errorf("correction replaced the goal or lost its revision: %s", result.Output)
			}
		case 6:
			if result.Code == "ok" || !strings.Contains(string(result.Output), "stale_revision") {
				t.Errorf("stale conversation correction was accepted: %s", item.Output)
			}
		case 8:
			var record coreexec.RecordReceipt
			if err := json.Unmarshal(result.Output, &record); err != nil || record.ID != corrected.RecordID || record.Revision != corrected.Revision || !strings.Contains(record.Markdown, "update only the parser") {
				t.Errorf("restart lost the corrected canonical record: %s", result.Output)
			}
		}
		emitResponse(w, "Goal operation checked.", true)
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
	admittedTurn(t, h, c, conversation, "admit-goal", "Update the library.")
	admittedTurn(t, h, c, conversation, "correct-goal", "Limit that work to the parser.")
	admittedTurn(t, h, c, conversation, "stale-goal", "Check whether the earlier revision is still current.")
	before, err := engine.Snapshot(h.ctx)
	if err != nil || len(before.Goals) != 1 || before.Goals[0].Summary != "update only the parser" || !before.Goals[0].DefinitionPending {
		t.Fatalf("conversation did not revise the running goal: %+v %v", before, err)
	}
	c.Close()
	stop()
	engine = start()
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "read-corrected-goal", "Read the corrected goal.")
	after, err := engine.Snapshot(h.ctx)
	if err != nil || len(after.Goals) != 1 || after.Goals[0].ID != before.Goals[0].ID || after.Goals[0].RecordRevision != before.Goals[0].RecordRevision || after.Goals[0].Scope.Includes != "parser" {
		t.Fatalf("restart changed the admitted goal: %+v %v", after, err)
	}
	c.Close()
	stop()
	stop = nil
	if calls.Load() != 8 {
		t.Fatalf("conversation did not complete all four operations: %d", calls.Load())
	}
}
