// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestResultToolsThroughAuthorityLedgerAndRecords(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	ms := canon.Migrations()
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	writer, err := results.OpenRecords(root, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	service, err := results.New(db, results.Config{Journal: results.LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: writer})
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Scope{PersonID: "person", AgentID: "agent", ConversationID: "conversation", TurnID: "turn"}
	identity := grant.Request{GrantID: "grant", MainID: "main", Epoch: 1, TaskID: "turn", Host: "executor", OSUser: "owner", Person: scope.PersonID, Agent: scope.AgentID}
	target, revision := "result:new:conversation", "0"
	auth, err := authority.Open(t.Context(), db, authority.Config{Epoch: 1, Resolve: func(_ context.Context, id string) (grant.Grant, grant.Environment, error) {
		permissions := []grant.Permission{}
		for _, tool := range []string{"present_result", "read_result", "save_result"} {
			permissions = append(permissions, grant.Permission{Tool: tool, Target: target, Write: true})
		}
		ident := identity
		ident.GrantID = id
		revisions := map[string]string{target: revision}
		if id == "read_result" {
			revisions = nil
		}
		return grant.Grant{Identity: ident, Expires: time.Now().Add(time.Hour), Scope: permissions, Revisions: revisions}, grant.Environment{Epoch: 1, Now: time.Now(), OSPermitted: true, Revision: revision}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	bind := func(intent *result.SaveRequest) map[string]loop.Tool {
		t.Helper()
		b, err := coreexec.ResultTools(tools.NewRegistry(auth, resultLeaser{}), service, scope, func(_ context.Context, name string, _ tools.Input) (tools.Call, error) {
			a := identity
			a.GrantID = name
			return tools.Call{Auth: a}, nil
		}, intent)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	bindings := bind(nil)
	callID := 0
	call := func(name string, arg any) loop.Result {
		t.Helper()
		callID++
		raw, _ := json.Marshal(arg)
		return bindings[name].Execute(t.Context(), provider.ToolCall{ID: strconv.Itoa(callID), Name: name, Arguments: string(raw)})
	}
	p := result.PresentRequest{Type: result.DraftType, Title: "Report", Summary: "Draft report.", Body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"The complete report."}],"from_summary":null}`)}
	out := call("present_result", p)
	if out.Code != loop.OK {
		t.Fatalf("present: %+v", out)
	}
	var c result.Card
	if err := json.Unmarshal(out.Output, &c); err != nil || c.ID == "" {
		t.Fatal(c, err)
	}
	if strings.Contains(string(out.Output), `"body"`) {
		t.Fatal("body leaked into acknowledgement")
	}
	target, revision = "result:"+c.ID, "1"
	out = call("read_result", result.ReadRequest{ID: c.ID})
	if out.Code != loop.OK {
		t.Fatalf("read: %+v", out)
	}
	p.ID = c.ID
	p.BaseRevision = 1
	out = call("present_result", p)
	if out.Code != loop.OK {
		t.Fatalf("revise: %+v", out)
	}
	revision = "2"
	out = call("present_result", p)
	if !strings.Contains(string(out.Output), `"stale_revision"`) {
		t.Fatalf("stale: %+v", out)
	}
	req := result.SaveRequest{ID: c.ID, Revision: 2, Destination: result.Destination{Kind: "resource"}}
	bindings = bind(&req)
	out = call("save_result", req)
	if out.Code != loop.OK {
		t.Fatalf("save: %+v", out)
	}
	first := string(out.Output)
	out = call("save_result", req)
	if out.Code != loop.OK || string(out.Output) != first {
		t.Fatalf("retry: %+v", out)
	}
	var saves, events int
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM result_saves").Scan(&saves); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event").Scan(&events)
	})
	if err != nil || saves != 1 || events != 3 {
		t.Fatal(saves, events, err)
	}
}
