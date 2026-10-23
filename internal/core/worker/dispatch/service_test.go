// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/proc"
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
	repo := t.TempDir()
	gitFixture(t, repo, "init", "-b", "dev")
	if err := os.WriteFile(filepath.Join(repo, "design.md"), []byte("# Result\nProduce result.txt.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, repo, "add", "design.md")
	gitFixture(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
	return Definition{Repository: repo, Base: "dev", Branch: "task-branch", What: "Produce result", Why: "Requested task", Scope: []string{"result.txt"}, Done: "file committed", DesignRefs: []string{"design.md#result"}, WriteArea: []string{"result.txt"}}
}
func TestDispatchWaitsForCommittedReceiptAndNeverReplaysUncertainWork(t *testing.T) {
	db := store(t)
	s, err := openService(t, Config{DB: db, Epoch: 1})
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
	restarted, err := openService(t, Config{DB: db, Epoch: 1})
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
	for _, name := range []string{"done", "writes", "design", "main", "master", "dev", "relative", "glob", "design_escape"} {
		t.Run(name, func(t *testing.T) {
			d := definition(t)
			switch name {
			case "done":
				d.Done = ""
			case "writes":
				d.WriteArea = nil
			case "design":
				d.DesignRefs = nil
			case "main", "master", "dev":
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

func TestDispatchReturnsAssignedBranchWithoutChangingInput(t *testing.T) {
	for _, alreadyNamed := range []bool{false, true} {
		t.Run(map[bool]string{false: "append", true: "preserve"}[alreadyNamed], func(t *testing.T) {
			db := store(t)
			origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"}
			id := admissionID(origin, "call")
			d := definition(t)
			want := d.Branch + "." + id
			if alreadyNamed {
				d.Branch = want
			}
			s, err := openService(t, Config{DB: db, Epoch: 1, Manage: func(_ context.Context, r Record) (Record, error) {
				// The persisted request remains available for idempotency; the
				// manager and worker must receive the actual assigned definition.
				packet, err := taskPacket(r)
				if err != nil || !strings.Contains(packet, want) {
					t.Errorf("task packet lost assigned branch: %s %v", packet, err)
				}
				return r, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(context.Background())
			raw, _ := json.Marshal(d)
			call := provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(raw)}
			tool := s.Tool(origin)
			first := tool.Execute(t.Context(), call)
			var result struct{ ID, Branch string }
			if first.Code != loop.OK || json.Unmarshal(first.Output, &result) != nil || result.ID != id || result.Branch != want {
				t.Fatalf("assigned branch absent or duplicated: %s", first.Output)
			}
			if again := tool.Execute(t.Context(), call); string(again.Output) != string(first.Output) {
				t.Fatal("retry changed branch", again)
			}
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 || records[0].Definition.Branch != d.Branch {
				t.Fatal("requested definition lost", err)
			}
		})
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

type testLeaser struct{}

func (testLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}
func openService(t *testing.T, cfg Config) (*Service, error) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Git, cfg.Activities, cfg.Environment = git, testLeaser{}, []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	return Open(t.Context(), cfg)
}
func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
