// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestWorkToolsBindAuthorityAndReplay(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	agent := workAgentFixture(t, r)
	r.agent = agent
	r.conversation = "conversation"
	r.personaMu = &sync.Mutex{}
	r.personaGrants = map[string]grant.Grant{}
	r.activities = lifecycle.NewRegistry(nil)
	var err error
	r.workArea, err = work.OpenArea(r.root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.workArea.Close(context.Background())
	r.work = work.NewWithArea(r.db, r.workArea, nil)
	r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: r.resolvePersonaGrant})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]loop.Tool{}
	if err = r.prepareWorkTools("turn", bindings); err != nil {
		t.Fatal(err)
	}
	call := func(id string, args any) loop.Result {
		b, _ := json.Marshal(args)
		result := bindings["prepare_work"].Execute(t.Context(), provider.ToolCall{ID: id, Name: "prepare_work", Arguments: string(b)})
		if len(r.personaGrants) != 0 {
			t.Fatal("grant leaked")
		}
		return result
	}
	input := coreexec.PrepareWork{TaskID: "task-1", Title: "fixture", Done: "file exists", RequiresApply: true}
	for _, field := range []string{"person", "agent", "grant_id", "artifact_roots"} {
		if result := call("forged-"+field, map[string]any{"task_id": input.TaskID, "title": input.Title, "done": input.Done, "workspace": input.Workspace, field: "forged"}); result.Code != loop.Invalid {
			t.Fatalf("forged %s: %+v", field, result)
		}
	}
	got := call("prepare", input)
	if got.Code != loop.OK {
		t.Fatalf("prepare: %+v", got)
	}
	retry := call("prepare", input)
	if retry.Code != loop.OK || string(retry.Output) != string(got.Output) {
		t.Fatalf("replay: %+v", retry)
	}
	card, err := r.work.Get(t.Context(), input.TaskID)
	if err != nil || card.Task.AgentID != agent || card.Task.ConversationID != r.conversation || len(card.Task.ArtifactRoots) != 1 {
		t.Fatalf("unbound task: %+v %v", card, err)
	}
	effect, err := r.personaAuthority.Effect(t.Context(), turn.EffectKey("turn", "prepare_work", "work:task-1", "prepare"))
	if err != nil || effect.Person != r.person || effect.Agent != agent {
		t.Fatalf("unbound effect: %+v %v", effect, err)
	}
	input.Done = "different"
	if result := call("conflict", input); result.Code == loop.OK {
		t.Fatal("changed task definition replaced existing allocation")
	}
}
