// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/records"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestManagerStepGraphTool(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	r.agent = workAgentFixture(t, r)
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	cfg := WorkConfig{Driver: func(*work.Engine) (work.Driver, error) { return parkedWorkDriver{}, nil }}
	if err = r.startWork(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	defer r.workEngine.close(t.Context())
	bindings := map[string]loop.Tool{}
	if _, err = r.prepareGoalWorkTools("turn", nil, bindings); err != nil {
		t.Fatal(err)
	}
	if bindings["submit_step_graph"].Execute == nil || bindings["read_step_graph"].Execute == nil {
		t.Fatal("manager cannot submit or read step graph revisions through running Core work tools")
	}
	admission := `{"outcome":"update library","done":"checks pass","scope":{"repositories":["repo"],"includes":"library"},"criteria":[{"id":"checks","statement":"library works","check":{"kind":"check","method":"test","target":"repo"}}],"no_service_reason":"library","tasks":[]}`
	result := bindings["dispatch_code_goal"].Execute(t.Context(), provider.ToolCall{ID: "goal", Name: "dispatch_code_goal", Arguments: admission})
	if result.Code != loop.OK {
		t.Fatalf("admit: %+v", result)
	}
	valid := `{"goal_id":"turn:goal","expected_revision":0,"steps":[{"id":"change","name":"로그인 화면 수정","done":"checks pass","repository":"repo","base":"dev","area":"src/**","worker":"stub","resources":{"cpu":1,"memory_mb":1,"disk_mb":1},"depends_on":[]}]}`
	submit := func(raw string) loop.Result {
		return bindings["submit_step_graph"].Execute(t.Context(), provider.ToolCall{ID: "graph", Name: "submit_step_graph", Arguments: raw})
	}
	result = submit(valid)
	if result.Code != loop.OK {
		t.Fatalf("graph admission: %+v", result)
	}
	saved := string(result.Output)
	if got := submit(valid); got.Code != loop.OK || string(got.Output) != saved {
		t.Fatalf("retry changed revision: %+v", got)
	}
	var bad map[string]any
	json.Unmarshal([]byte(valid), &bad)
	bad["expected_revision"] = 1
	bad["steps"].([]any)[0].(map[string]any)["name"] = "Fix #762"
	raw, _ := json.Marshal(bad)
	refused := submit(string(raw))
	if refused.Code == loop.OK || string(refused.Output) != `{"code":"graph_invalid_name"}` {
		t.Fatalf("name refusal: %+v", refused)
	}
	got := bindings["read_step_graph"].Execute(t.Context(), provider.ToolCall{ID: "read", Arguments: `{"goal_id":"turn:goal"}`})
	if got.Code != loop.OK || string(got.Output) != saved {
		t.Fatalf("refusal changed graph: %+v", got)
	}
	foreign := *r
	foreign.agent = "foreign"
	outside := map[string]loop.Tool{}
	foreign.prepareGoalWorkTools("foreign-turn", nil, outside)
	if got := outside["read_step_graph"].Execute(t.Context(), provider.ToolCall{Arguments: `{"goal_id":"turn:goal"}`}); got.Code == loop.OK {
		t.Fatal("foreign manager read the graph")
	}
}
