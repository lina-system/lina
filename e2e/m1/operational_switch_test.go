// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
	takeover "github.com/thisisjun786/lina/tools/crw-takeover/switcher"
)

// Live switch operations go through the real Core binary. The executor fixture
// uses the same work admission API as the engine and a real child process; only
// the external coordinator is synthetic. No installed CRW or account is used.
func TestM3OperationalSwitch(t *testing.T) {
	var scenario struct {
		Capability, Version string
		TaskID              string `json:"task_id"`
		SwitchID            string `json:"switch_id"`
		Title, Done         string
	}
	data, err := os.ReadFile("../m3/operational-switch.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h, initialStart := installedConversationHarness(t, "http://127.0.0.1:1")
	scenarioContext, cancelScenario := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancelScenario()
	h.ctx = scenarioContext
	core := initialStart(false)
	identityClient, err := clientapi.Dial(h.ctx, h.socket, "test")
	if err != nil {
		t.Fatal(err)
	}
	person := identityClient.Welcome.PersonID
	identityClient.Close()
	waitCoreReady(t, h)
	h.stopCore(core)
	seedMethodologyAgents(t, h, person, "")
	path := filepath.Join(h.root, "canon", "canon.sqlite")
	withWork := func(f func(*work.Service)) {
		ctl, e := ctlfiles.Open(h.root)
		if e != nil {
			t.Fatal(e)
		}
		defer ctl.Close()
		if e = ctl.Acquire(); e != nil {
			t.Fatal(e)
		}
		_, epoch, e := canon.Inspect(h.ctx, path)
		if e != nil {
			t.Fatal(e)
		}
		db, e := canon.Open(h.ctx, canon.Config{Path: path, Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close(context.Background())
		f(work.New(db))
	}
	dir := filepath.Join(h.root, "work", "tasks", scenario.TaskID)
	source := t.TempDir()
	git := func(directory string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(h.ctx, "git", args...)
		cmd.Dir = directory
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + source, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
		output, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v\n%s", args, e, output)
		}
		return strings.TrimSpace(string(output))
	}
	git(source, "init", "-b", "main")
	original := filepath.Join(source, "original.txt")
	if err = os.WriteFile(original, []byte("retained source"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", "original.txt")
	git(source, "commit", "-m", "Retain original work")
	branch := "task/" + scenario.TaskID
	git(source, "worktree", "add", "-b", branch, dir)
	originalHead := git(source, "rev-parse", "HEAD")
	originalWorktrees := git(source, "worktree", "list", "--porcelain")
	failedTask := scenario.TaskID + "-failed"
	failedDir := t.TempDir()
	run := work.Run{ID: "execution", TaskID: scenario.TaskID, Generation: 1, Worker: "fixture", ThreadID: "thread", TurnID: "turn", NodeID: "local"}
	withWork(func(s *work.Service) {
		if e := s.Create(h.ctx, work.Task{ID: scenario.TaskID, PersonID: person, AgentID: "agent", ConversationID: "conversation", Title: scenario.Title, Done: scenario.Done, ArtifactRoots: []string{dir}, ExternalControl: true}); e != nil {
			t.Fatal(e)
		}
		if e := s.Start(h.ctx, run); !errors.Is(e, work.ErrControlHeld) {
			t.Fatalf("unapproved source task ran: %v", e)
		}
		if e := s.Create(h.ctx, work.Task{ID: failedTask, PersonID: person, AgentID: "agent", ConversationID: "conversation", Title: scenario.Title, Done: scenario.Done, ArtifactRoots: []string{failedDir}, ExternalControl: true}); e != nil {
			t.Fatal(e)
		}
	})
	var providerCalls atomic.Int32
	var control map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		n := providerCalls.Add(1)
		if n == 2 || n >= 4 {
			var item struct{ Output string }
			var result struct {
				Code   string
				Output map[string]any
			}
			if len(req.Input) == 0 {
				t.Error("missing tool response")
				return
			}
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &item)
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
				t.Error(err)
				return
			}
			expected := "ok"
			if n == 2 || n == 5 {
				expected = "failed"
			}
			if result.Code != expected {
				t.Errorf("worker step %d: %s", n, item.Output)
				emitResponse(w, "failed", true)
				return
			}
			if n >= 4 && n != 5 {
				control = map[string]any{"session": result.Output["session"], "turn": result.Output["turn"], "generation": result.Output["generation"], "binding": result.Output["binding"]}
			}
			if n == 9 && result.Output["output"] != "LINA_WORKER_RESULT" {
				t.Error("missing actual worker result", result.Output)
			}
		}
		emit := func(name string, args map[string]any) {
			emitPersonaCall(w, fmt.Sprintf("switch-worker-%d", n), name, args)
		}
		switch n {
		case 1, 3:
			emit("worker_start", map[string]any{"workdir": dir, "task_packet": "switch refusal and terminal connection failure"})
		case 2:
			emitResponse(w, "external owner retained", true)
		case 4:
			control["text"] = "continue while waiting"
			emit("worker_input", control)
		case 5:
			delete(control, "text")
			control["quote"] = "Stop this worker"
			emit("worker_interrupt", control)
		case 6, 8:
			emit("worker_snapshot", map[string]any{"session": control["session"]})
		case 7:
			control["text"] = "produce result"
			emit("worker_input", control)
		default:
			emitResponse(w, "transferred work produced a result", true)
		}
	}))
	defer server.Close()
	bin := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(executable, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	var c *clientapi.Client
	start := func() {
		core = h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=1", "CODEX_HOME=/fixture/codex-home", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		capability := protocol.Capability{Name: scenario.Capability, Version: scenario.Version}
		c, err = clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{capability})
		if err != nil {
			t.Fatal(err)
		}
		connection := c
		t.Cleanup(func() { connection.Close() })
		if !slices.Contains(c.Welcome.Capabilities, capability) {
			t.Fatal("running Core does not expose the approved operational switch")
		}
	}
	stop := func() { c.Close(); h.stopCore(core) }
	call := func(ctx context.Context, q protocol.WorkSwitchRequest) (protocol.WorkSwitchResult, error) {
		id, e := c.Work(ctx, q)
		if e != nil {
			return protocol.WorkSwitchResult{}, e
		}
		frame, e := c.Receive(ctx)
		if e != nil {
			return protocol.WorkSwitchResult{}, e
		}
		if frame.ReplyTo == nil || *frame.ReplyTo != id {
			t.Fatal("uncorrelated switch result")
		}
		if frame.Error != nil {
			return protocol.WorkSwitchResult{}, &clientapi.Refusal{Cause: *frame.Error}
		}
		v, ok := frame.Payload.(protocol.WorkSwitchResult)
		if !ok {
			t.Fatalf("switch reply: %T", frame.Payload)
		}
		return v, nil
	}
	manifest := takeover.Manifest{SwitchID: scenario.SwitchID, Tasks: []string{scenario.TaskID}, Snapshot: strings.Repeat("b", 64), Coverage: "fixture parent and flow inventory quiesced", Executable: "/fixture/relay", StateDirectory: "/fixture/state", Socket: "/fixture/socket"}
	enabled, running := true, true
	failDisable := false
	var externalCalls []string
	command := func(_ context.Context, action string) ([]byte, error) {
		externalCalls = append(externalCalls, action)
		switch action {
		case "disable":
			enabled = false
			running = false
			if failDisable {
				failDisable = false
				return nil, errors.New("disable response lost after stopping")
			}
		case "enable":
			enabled = true
		case "start":
			running = true
		}
		lock := "free"
		if running {
			lock = "held"
		}
		return json.Marshal(takeover.Observation{Enabled: &enabled, Running: &running, Lock: lock, StateDirectory: manifest.StateDirectory, ScopeAuthority: "production", Conflicts: []any{}})
	}
	procedure := takeover.Procedure{Manifest: manifest, Journal: filepath.Join(h.root, "switch-journal.json"), Core: call, Command: command}
	start()
	if err = procedure.Apply(h.ctx); err == nil || len(externalCalls) != 0 {
		t.Fatal("external effects without owner approval", err, externalCalls)
	}
	failedManifest := manifest
	failedManifest.SwitchID += "-failed"
	failedManifest.Tasks = []string{failedTask}
	failedDigest := failedManifest.Digest()
	if _, err = call(h.ctx, protocol.WorkSwitchRequest{Operation: "approve_switch", SwitchID: failedManifest.SwitchID, Tasks: &failedManifest.Tasks, Snapshot: &failedDigest}); err != nil {
		t.Fatal(err)
	}
	failedProcedure := takeover.Procedure{Manifest: failedManifest, Journal: filepath.Join(h.root, "failed-switch-journal.json"), Core: call, Command: command}
	failDisable = true
	if err = failedProcedure.Apply(h.ctx); err == nil {
		t.Fatal("failed transfer reported success")
	}
	failedStatus, err := call(h.ctx, protocol.WorkSwitchRequest{Operation: "switch_status", SwitchID: failedManifest.SwitchID})
	if err != nil || failedStatus.State != "rolled_back" || len(failedStatus.Events) != 3 || !enabled || !running {
		t.Fatalf("failed transfer did not explicitly restore ownership: %+v enabled=%v running=%v %v", failedStatus, enabled, running, err)
	}
	digest := manifest.Digest()
	approval, err := call(h.ctx, protocol.WorkSwitchRequest{Operation: "approve_switch", SwitchID: scenario.SwitchID, Tasks: &manifest.Tasks, Snapshot: &digest})
	if err != nil || approval.Approval == "" {
		t.Fatal("approval", err)
	}
	stop()
	withWork(func(s *work.Service) {
		if e := s.Start(h.ctx, run); !errors.Is(e, work.ErrControlHeld) {
			t.Fatal("approval alone released task", e)
		}
	})
	start()
	conversation, conversationID := conversationClient(t, h)
	admittedTurn(t, h, conversation, conversationID, "switch-blocked", "Start the transferred worker")
	if providerCalls.Load() != 2 {
		t.Fatal("missing live worker refusal", providerCalls.Load())
	}
	if err = procedure.Apply(h.ctx); err != nil {
		t.Fatal(err)
	}
	if enabled || running {
		t.Fatal("two active coordinators")
	}
	admittedTurn(t, h, conversation, conversationID, "switch-execute", "Stop this worker after starting it, then resume to produce the result")
	if providerCalls.Load() != 9 {
		t.Fatal("worker path incomplete", providerCalls.Load())
	}
	conversation.Close()
	stop()
	var receipt work.Receipt
	withWork(func(s *work.Service) {
		if e := s.Start(h.ctx, run); e != nil {
			t.Fatal(e)
		}
		output := filepath.Join(dir, "result.txt")
		bytes, e := os.ReadFile(output)
		if e != nil || string(bytes) != "LINA_WORKER_RESULT" {
			t.Fatal("real Core worker artifact", e)
		}
		now := time.Now().UTC()
		hash := fmt.Sprintf("%x", sha256.Sum256(bytes))
		receipt = work.Receipt{SchemaVersion: 1, RelationshipID: run.TaskID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: output, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "report", TaskID: run.TaskID, NodeID: run.NodeID, Path: output, SHA256: hash, ObservedAt: now}}}
		receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
		receipt.EventID = work.ReceiptID(receipt)
		if e = s.Submit(h.ctx, run.ID, receipt); e != nil {
			t.Fatal(e)
		}
	})
	start()
	// Restart makes the outstanding execution unknown. A completed artifact or
	// a receipt alone is not permission to restart the previous coordinator.
	if err = procedure.Rollback(h.ctx); err == nil {
		t.Fatal("rollback released an unknown execution")
	}
	if enabled || running {
		t.Fatal("rollback restored external owner before drain")
	}
	status, err := call(h.ctx, protocol.WorkSwitchRequest{Operation: "switch_status", SwitchID: scenario.SwitchID})
	if err != nil || status.State != "rollback_pending" {
		t.Fatal(status, err)
	}
	stop()
	withWork(func(s *work.Service) {
		if e := s.Finish(h.ctx, run.TaskID, run.ID, "completed"); e != nil {
			t.Fatal(e)
		}
	})
	start()
	if err = procedure.Rollback(h.ctx); err != nil {
		t.Fatal(err)
	}
	if !enabled || !running {
		t.Fatal("previous coordinator not restored")
	}
	final, err := call(h.ctx, protocol.WorkSwitchRequest{Operation: "switch_status", SwitchID: scenario.SwitchID})
	if err != nil || final.State != "rolled_back" || len(final.Events) != 4 || final.Approval != approval.Approval {
		t.Fatalf("rollback audit: %+v %v", final, err)
	}
	stop()
	withWork(func(s *work.Service) {
		next := run
		next.ID = "second"
		next.Generation = 2
		if e := s.Start(h.ctx, next); !errors.Is(e, work.ErrControlHeld) {
			t.Fatal("rollback gate lost", e)
		}
		card, e := s.Get(h.ctx, run.TaskID)
		if e != nil || card.Receipt == nil || !reflect.DeepEqual(*card.Receipt, receipt) {
			t.Fatal("receipt was not retained", e)
		}
		failedCard, e := s.Get(h.ctx, failedTask)
		if e != nil || !failedCard.Task.ExternalControl || failedCard.Task.Title != scenario.Title {
			t.Fatal("failed transfer lost the imported task", e)
		}
	})
	bytes, err := os.ReadFile(original)
	if err != nil || string(bytes) != "retained source" {
		t.Fatal("original lost", err)
	}
	bytes, err = os.ReadFile(receipt.Manifest[0].Path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(bytes)) != receipt.Manifest[0].SHA256 {
		t.Fatal("worker output lost", err)
	}
	if _, err = os.Stat(procedure.Journal); err != nil {
		t.Fatal("rollback journal lost", err)
	}
	if _, err = os.Stat(failedProcedure.Journal); err != nil {
		t.Fatal("failed transfer journal lost", err)
	}
	if got := git(source, "rev-parse", "HEAD"); got != originalHead {
		t.Fatal("original branch changed", got)
	}
	if got := git(source, "rev-parse", "refs/heads/"+branch); got != originalHead {
		t.Fatal("task branch changed", got)
	}
	if got := git(source, "worktree", "list", "--porcelain"); got != originalWorktrees {
		t.Fatal("worktree registration changed", got)
	}
	if got := git(dir, "symbolic-ref", "HEAD"); got != "refs/heads/"+branch {
		t.Fatal("worktree branch changed", got)
	}
	bytes, err = os.ReadFile(filepath.Join(dir, "original.txt"))
	if err != nil || string(bytes) != "retained source" {
		t.Fatal("worktree original lost", err)
	}
}
