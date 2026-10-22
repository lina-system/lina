// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func store(t *testing.T) *canon.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func definition(t *testing.T) Definition {
	return Definition{Repository: t.TempDir(), Base: "dev", Branch: "task-branch", What: "Produce result", Why: "Requested task", Scope: []string{"result.txt"}, Done: "file committed", DesignRefs: []string{"design.md#result"}, WriteArea: []string{"result.txt"}}
}
func TestDispatchWaitsForCommittedReceiptAndNeverReplaysUncertainWork(t *testing.T) {
	db := store(t)
	s, err := Open(t.Context(), Config{DB: db, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	d := definition(t)
	raw, _ := json.Marshal(d)
	tool := s.Tool(Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"})
	call := provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(raw)}
	first := tool.Execute(t.Context(), call)
	if first.Code != loop.OK {
		t.Fatal(first)
	}
	second := tool.Execute(t.Context(), call)
	if string(second.Output) != string(first.Output) {
		t.Fatal("retry changed receipt")
	}
	d.Done = "different task"
	altered, _ := json.Marshal(d)
	original := call.Arguments
	call.Arguments = string(altered)
	if tool.Execute(t.Context(), call).Code != loop.Denied {
		t.Fatal("same call id substituted a different task")
	}
	call.Arguments = original
	call.ID = "overlap"
	if tool.Execute(t.Context(), call).Code != loop.Denied {
		t.Fatal("overlapping repository admitted")
	}
	if err = s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 || records[0].State != "queued" {
		t.Fatalf("uncommitted tool output launched work: %+v %v", records, err)
	}
	r := records[0]
	r.State = "running"
	if err = s.save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(t.Context(), Config{DB: db, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	records, err = restarted.records(t.Context())
	if err != nil || records[0].State != "unknown" || records[0].Code != "interrupted_execution" {
		t.Fatalf("replayed uncertain execution: %+v %v", records, err)
	}
}
func TestDefinitionRefusesMissingScopeAndProtectedBranches(t *testing.T) {
	for _, name := range []string{"done", "writes", "design", "main", "dev", "relative", "glob", "design_escape"} {
		t.Run(name, func(t *testing.T) {
			d := definition(t)
			switch name {
			case "done":
				d.Done = ""
			case "writes":
				d.WriteArea = nil
			case "design":
				d.DesignRefs = nil
			case "main", "dev":
				d.Branch = name
			case "relative":
				d.Repository = "repo"
			case "glob":
				d.WriteArea = []string{"["}
			case "design_escape":
				d.DesignRefs = []string{"../private#data"}
			}
			if validate(d) == nil {
				t.Fatal("invalid task accepted")
			}
		})
	}
	if allowedFiles("result.txt\nprivate.txt", []string{"result.txt"}) {
		t.Fatal("out of scope file accepted")
	}
}
