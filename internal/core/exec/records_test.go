// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestRecordToolsScopeAndDurableReplay(t *testing.T) {
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
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	repo, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	auth := grant.Request{GrantID: "grant", MainID: "main", Epoch: 1, TaskID: "turn", Host: "synthetic", OSUser: "synthetic", Person: "person", Agent: "agent", Revision: "new"}
	target := "records:new"
	name := "write_record"
	service, err := authority.Open(t.Context(), db, authority.Config{Epoch: 1, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: name, Target: target, Write: name == "write_record"}}, Revisions: map[string]string{target: auth.Revision}}, grant.Environment{Epoch: 1, Now: time.Now(), OSPermitted: true, Revision: auth.Revision}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(service, lifecycle.NewRegistry(nil))
	specs, err := coreexec.RegisterRecords(registry, repo, records.Actor{Person: "person", Agent: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"path", "person_id", "agent_id"} {
		raw := json.RawMessage(`{"id":"","revision":"","kind":"memo","title":"Note","note":"Body","` + field + `":"injected"}`)
		if _, err := specs[1].Schema(raw); err == nil {
			t.Fatal("model supplied authority", field)
		}
	}
	call := func(key string, args any) tools.Result {
		t.Helper()
		raw, _ := json.Marshal(args)
		r, err := registry.Dispatch(t.Context(), tools.Call{Key: key, Name: name, Auth: auth, Args: raw})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	request := coreexec.RecordWrite{Kind: "memo", Title: "Notes", Note: "First"}
	first := call("create", request)
	if first.State != tools.Applied {
		t.Fatalf("create: %+v", first)
	}
	before, err := repo.Head(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replay := call("create", request)
	after, err := repo.Head(t.Context())
	if err != nil || before != after || string(first.Body) != string(replay.Body) {
		t.Fatal("replay wrote twice", err)
	}
	effect, err := service.Effect(t.Context(), "create")
	if err != nil || effect.State != tools.Applied {
		t.Fatal("missing receipt", effect, err)
	}
	var receipt coreexec.RecordReceipt
	if err = json.Unmarshal(first.Body, &receipt); err != nil {
		t.Fatal(err)
	}
	name = "read_record"
	target = "record:" + receipt.ID
	got := call("read", coreexec.RecordRead{ID: receipt.ID})
	if got.State != tools.Applied || string(got.Body) != string(first.Body) {
		t.Fatal("read differs", got)
	}
	// A different person cannot read an existing saved record.
	foreign := tools.NewRegistry(service, lifecycle.NewRegistry(nil))
	if _, err = coreexec.RegisterRecords(foreign, repo, records.Actor{Person: "other-person", Agent: "other-agent"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(coreexec.RecordRead{ID: receipt.ID})
	refused, err := foreign.Dispatch(t.Context(), tools.Call{Key: "foreign-read", Name: name, Auth: auth, Args: raw})
	if err != nil || refused.State != tools.Failed || strings.Contains(string(refused.Body), "First") {
		t.Fatalf("foreign read: %+v %v", refused, err)
	}
	name = "write_record"
	auth.Revision = receipt.Revision
	request.ID = receipt.ID
	request.Revision = receipt.Revision
	request.Note = "Changed"
	updated := call("update", request)
	if updated.State != tools.Applied {
		t.Fatal(updated)
	}
	stale := call("stale", request)
	if stale.State != tools.Failed || !strings.Contains(string(stale.Body), "stale_revision") {
		t.Fatal("store accepted stale base", stale)
	}
}
