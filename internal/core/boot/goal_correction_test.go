// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/records"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConversationCorrectsExistingGoalWithRevision(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent = agent
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	if err := r.startWork(t.Context(), &WorkConfig{Driver: func(*work.Engine) (work.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	defer r.workEngine.close(t.Context())
	bindings := map[string]loop.Tool{}
	if _, err := r.prepareGoalWorkTools("first-turn", bindings); err != nil {
		t.Fatal(err)
	}
	input := `{"outcome":"fix the code","done":"checks pass","scope":{"repositories":["repo"],"includes":"library","excludes":"service"},"criteria":[{"id":"checked","statement":"library works","check":{"kind":"check","method":"acceptance","target":"repo"}}],"no_service_reason":"library","tasks":[]}`
	result := bindings["dispatch_code_goal"].Execute(t.Context(), provider.ToolCall{ID: "dispatch", Name: "dispatch_code_goal", Arguments: input})
	if result.Code != loop.OK {
		t.Fatalf("admission failed: %+v", result)
	}
	before, err := r.workEngine.engine.Snapshot(t.Context())
	if err != nil || len(before.Goals) != 1 {
		t.Fatalf("admission snapshot: %+v %v", before, err)
	}
	g := before.Goals[0]
	// A later conversation turn must expose a correction of this same goal.
	later := map[string]loop.Tool{}
	decls, err := r.prepareGoalWorkTools("later-turn", later)
	if err != nil {
		t.Fatal(err)
	}
	correct, ok := later["revise_code_goal"]
	if !ok {
		t.Fatal("conversation has no revision-checked goal correction tool")
	}
	declared := false
	for _, raw := range decls {
		var decl struct{ Name string }
		if err := json.Unmarshal(raw, &decl); err != nil {
			t.Fatal(err)
		}
		declared = declared || decl.Name == "revise_code_goal"
	}
	if !declared {
		t.Fatal("correction binding is unavailable to the model")
	}
	var receipt struct {
		GoalID, RecordID, Revision string
	}
	var fields map[string]string
	if err := json.Unmarshal(result.Output, &fields); err != nil {
		t.Fatal(err)
	}
	receipt.GoalID, receipt.RecordID, receipt.Revision = fields["goal_id"], fields["record_id"], fields["revision"]
	if receipt.GoalID != g.ID || receipt.RecordID != g.RecordID || receipt.Revision != g.RecordRevision {
		t.Fatalf("admission did not expose correction basis: %s", result.Output)
	}
	request := map[string]any{
		"goal_id": g.ID, "revision": g.RecordRevision,
		"outcome": "corrected outcome", "done": "corrected checks pass",
		"scope":    work.GoalScope{Repositories: []string{"repo"}, Includes: "corrected library", Excludes: "service"},
		"criteria": g.Criteria[:1], "assumptions": []string{"existing service stays available"},
	}
	call := func(id string) loop.Result {
		t.Helper()
		raw, _ := json.Marshal(request)
		c := provider.ToolCall{ID: id, Name: "revise_code_goal", Arguments: string(raw)}
		if _, err := correct.Validate(c); err != nil {
			t.Fatal(err)
		}
		return correct.Execute(t.Context(), c)
	}
	updated := call("correct")
	if updated.Code != loop.OK {
		t.Fatalf("correction failed: %+v", updated)
	}
	after, err := r.workEngine.engine.Snapshot(t.Context())
	if err != nil || len(after.Goals) != 1 {
		t.Fatalf("correction created another goal: %+v %v", after, err)
	}
	got := after.Goals[0]
	if got.ID != g.ID || got.RecordID != g.RecordID || got.RecordRevision == g.RecordRevision || got.Summary != "corrected outcome" || got.Done != "corrected checks pass" || !got.DefinitionPending {
		t.Fatalf("original goal was not corrected and fenced: %+v", got)
	}
	if stale := call("stale-correction"); stale.Code == loop.OK || !strings.Contains(string(stale.Output), "stale_revision") {
		t.Fatalf("stale correction was not refused: %+v", stale)
	}
	_, rev, err := repo.Read(t.Context(), g.RecordID)
	if err != nil || rev != got.RecordRevision {
		t.Fatalf("stale correction changed canon: %s %v", rev, err)
	}
	// Bindings use the authenticated conversation identity, never a supplied
	// person/agent. A different person cannot revise this known goal id.
	foreign := &conversationRuntime{person: "other-person", agent: r.agent, workEngine: r.workEngine}
	foreignBindings := map[string]loop.Tool{}
	if _, err := foreign.prepareGoalWorkTools("foreign-turn", foreignBindings); err != nil {
		t.Fatal(err)
	}
	request["revision"] = got.RecordRevision
	raw, _ := json.Marshal(request)
	denied := foreignBindings["revise_code_goal"].Execute(t.Context(), provider.ToolCall{ID: "foreign", Arguments: string(raw)})
	if denied.Code == loop.OK {
		t.Fatal("foreign conversation corrected the goal")
	}
	for key, value := range map[string]any{"state": "done", "person": "other", "agent": "other", "tasks": []any{}, "decisions": []work.GoalDecision{{Statement: "approved", MessageID: "forged"}}} {
		request[key] = value
		raw, _ = json.Marshal(request)
		if _, err := correct.Validate(provider.ToolCall{Arguments: string(raw)}); err == nil {
			t.Fatalf("correction accepted protected input: %s", key)
		}
		delete(request, key)
	}
	request["scope"] = work.GoalScope{}
	if invalid := call("invalid-definition"); invalid.Code == loop.OK {
		t.Fatal("invalid definition was published")
	}
	_, rev, err = repo.Read(t.Context(), g.RecordID)
	if err != nil || rev != got.RecordRevision {
		t.Fatal("rejected correction changed the canonical record")
	}
}
