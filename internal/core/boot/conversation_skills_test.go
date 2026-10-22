// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConversationSkillAuthorityAndReceipts(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.main, r.agent, r.epoch = "main", agent, 1
	r.personaMu, r.personaGrants = &sync.Mutex{}, map[string]grant.Grant{}
	r.activities = lifecycle.NewRegistry(nil)
	var err error
	r.skills, err = skills.Open(t.TempDir(), r.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.skills.Close() })
	r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: r.resolvePersonaGrant})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]loop.Tool{}
	if err := r.prepareSkillTools("turn", bindings); err != nil {
		t.Fatal(err)
	}
	call := func(id, name string, args any) loop.Result {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		result := bindings[name].Execute(t.Context(), provider.ToolCall{ID: id, Name: name, Arguments: string(raw)})
		if len(r.personaGrants) != 0 {
			t.Fatal("skill grant outlived dispatch")
		}
		return result
	}
	text := "---\nname: example\ndescription: Synthetic procedure.\n---\nCheck the input.\n"
	patch := skills.Patch{ID: "example", Files: skills.Bundle{"SKILL.md": {Text: text}}}
	for _, field := range []string{"person", "agent", "grant_id", "target", "permissions"} {
		forged := map[string]any{"id": patch.ID, "expected": 0, "files": patch.Files, field: "foreign"}
		if result := call("forged-"+field, "write_skill", forged); result.Code != loop.Invalid {
			t.Fatalf("forged %s accepted: %+v", field, result)
		}
	}
	written := call("write", "write_skill", patch)
	if written.Code != loop.OK {
		t.Fatal(written)
	}
	retry := call("write", "write_skill", patch)
	if retry.Code != loop.OK || string(retry.Output) != string(written.Output) {
		t.Fatalf("receipt replay: %+v", retry)
	}
	if result := call("stale", "write_skill", patch); result.Code != loop.Denied {
		t.Fatalf("stale write not denied: %+v", result)
	}
	effect, err := r.personaAuthority.Effect(t.Context(), turn.EffectKey("turn", "write_skill", "skill:example", "write"))
	if err != nil || effect.Person != r.person || effect.Agent != agent || effect.TaskID != "turn" || effect.Epoch != 1 || effect.Target != "skill:example" {
		t.Fatalf("effect lost trusted identity: %+v %v", effect, err)
	}
	var receipt skills.Applied
	if err := json.Unmarshal(written.Output, &receipt); err != nil {
		t.Fatal(err)
	}
	if result := call("undo", "undo_skill", receipt.Undo); result.Code != loop.OK {
		t.Fatal(result)
	}
	if result := call("stale-undo", "undo_skill", receipt.Undo); result.Code != loop.Denied {
		t.Fatalf("stale undo not denied: %+v", result)
	}
	// Immutable handles remain readable even when the active head was removed.
	read := call("read-old", "read_skill", receipt.Version)
	var body skills.Body
	if err := json.Unmarshal(read.Output, &body); err != nil || read.Code != loop.OK || body.Files["SKILL.md"].Text != text {
		t.Fatalf("immutable read: %+v %v", read, err)
	}
	entries, err := r.skills.Catalog(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("undo catalog: %+v %v", entries, err)
	}
}
