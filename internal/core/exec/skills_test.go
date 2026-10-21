// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestSkillToolsCommitNoticeReadUndoAndRefuseCode(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: skills.Schema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := skills.Open(root, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth := grant.Request{GrantID: "grant", MainID: "main", Epoch: 1, TaskID: "task", Host: "synthetic", OSUser: "synthetic", Person: "person", Agent: "agent", Revision: "0"}
	service, err := authority.Open(t.Context(), db, authority.Config{Epoch: 1, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "write_skill", Target: "skill:example", Write: true}, {Tool: "undo_skill", Target: "skill:example", Write: true}, {Tool: "read_skill", Target: "skill:example"}, {Tool: "skill_index", Target: "skills"}}, Revisions: map[string]string{"skill:example": auth.Revision}}, grant.Environment{Epoch: 1, Now: time.Now(), OSPermitted: true, Revision: auth.Revision}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(service, lifecycle.NewRegistry(nil))
	if _, err = coreexec.RegisterSkills(registry, store); err != nil {
		t.Fatal(err)
	}
	call := func(key, name string, args any) tools.Result {
		t.Helper()
		raw, _ := json.Marshal(args)
		r, err := registry.Dispatch(t.Context(), tools.Call{Key: key, Name: name, Auth: auth, Args: raw})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	text := "---\nname: example\ndescription: Use for a synthetic procedure.\n---\nCheck input.\n"
	patch := skills.Patch{ID: "example", Files: skills.Bundle{"SKILL.md": {Text: text}}}
	a := call("create", "write_skill", patch)
	if a.State != tools.Applied {
		t.Fatal(a)
	}
	var applied skills.Applied
	if err = json.Unmarshal(a.Body, &applied); err != nil || applied.Notice == "" {
		t.Fatalf("notice: %s %v", a.Body, err)
	}
	// The response is visible only after both the skill version and effect receipt.
	effect, err := service.Effect(t.Context(), "create")
	if err != nil || effect.State != tools.Applied {
		t.Fatalf("receipt: %+v %v", effect, err)
	}
	body, err := store.Read(t.Context(), applied.Version)
	if err != nil || body.Files["SKILL.md"].Text != text {
		t.Fatalf("committed: %+v %v", body, err)
	}
	replay := call("create", "write_skill", patch)
	if string(replay.Body) != string(a.Body) {
		t.Fatal("replay changed receipt")
	}
	auth.Revision = a.Revision
	index := call("index", "skill_index", struct{}{})
	var entries []skills.Version
	if err = json.Unmarshal(index.Body, &entries); err != nil || len(entries) != 1 {
		t.Fatalf("index: %s %v", index.Body, err)
	}
	read := call("read", "read_skill", entries[0])
	if read.State != tools.Applied {
		t.Fatal(read)
	}
	patch.Expected = 1
	patch.Files["helper.sh"] = skills.File{Text: "echo example"}
	rejected := call("code", "write_skill", patch)
	if rejected.State != tools.Failed {
		t.Fatalf("code adopted: %+v", rejected)
	}
	var proposal coreexec.SkillFailure
	if err = json.Unmarshal(rejected.Body, &proposal); err != nil || proposal.Code != "skill_requires_explicit_acceptance" || proposal.Proposal == nil || proposal.Proposal.Type != "summary" {
		t.Fatalf("proposal: %s %v", rejected.Body, err)
	}
	undone := call("undo", "undo_skill", applied.Undo)
	if undone.State != tools.Applied {
		t.Fatal(undone)
	}
	entries, err = store.Catalog(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("undo: %+v %v", entries, err)
	}
}
