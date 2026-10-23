// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

// This scenario launches the real Core command. Only the model, ACP agent and
// GitHub boundary are synthetic; dispatch, worktrees, commits and cards are real.
func TestACPDecisionsThroughRunningCore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("installed worker capacity requires Linux host sampling")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Fatal("worker load fixture requires bubblewrap:", err)
	}
	binary := filepath.Join(t.TempDir(), "lina")
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-ldflags=-w", "-o", binary, "./cmd/lina")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Core: %v\n%s", err, out)
	}
	nodeBinary := filepath.Join(t.TempDir(), "lina-node")
	nodeBuild := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-ldflags=-w", "-o", nodeBinary, "./cmd/lina-node")
	nodeBuild.Dir = "../.."
	if out, err := nodeBuild.CombinedOutput(); err != nil {
		t.Fatalf("build Node: %v\n%s", err, out)
	}
	for _, scenario := range []struct{ name, worker, binary, mode string }{
		{"once", "gemini", "gemini", "once"},
		{"deny", "cursor", "cursor-agent", "deny"},
		{"cancel", "grok", "grok", "cancel"},
		{"restart", "gemini", "gemini", "restart"},
		{"form", "claude-acp", "claude-agent-acp", "form"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mode := scenario.mode
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			root, err := os.MkdirTemp("", "lina-acp-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			state := filepath.Join(root, "state")
			h := &harness{ctx: ctx, root: state}
			bin, repo, fixture := filepath.Join(root, "bin"), filepath.Join(root, "repo"), filepath.Join(root, "fixture")
			for _, dir := range []string{bin, repo, fixture} {
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(file, body string, perm os.FileMode) {
				t.Helper()
				if err = os.WriteFile(file, []byte(body), perm); err != nil {
					t.Fatal(err)
				}
			}
			// A closed PATH proves ACP-only discovery. No installed user's worker or gh
			// can be selected, including by the chooser and version probes.
			for _, name := range []string{"git", "node", "sh"} {
				p, e := exec.LookPath(name)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(p, filepath.Join(bin, name)); e != nil {
					t.Fatal(e)
				}
			}
			names := []string{scenario.binary, "gh"}
			if scenario.worker == "claude-acp" {
				names = append(names, "claude")
			}
			for _, name := range names {
				write(filepath.Join(bin, name), acpDecisionsScript, 0700)
			}
			env := []string{"PATH=" + bin, "HOME=" + root, "XDG_CONFIG_HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C", "LINA_ACP_FIXTURE=" + fixture, "LINA_ACP_CASE=" + mode, "LINA_ACP_WORKER=" + scenario.worker}
			write(filepath.Join(bin, "systemctl"), "#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\ncase \"$2\" in show) printf 'not-found\\n';; daemon-reload|enable) exit 0;; *) exit 97;; esac\n", 0700)
			bundle := makeInstallBundle(t, root, binary, nodeBinary)
			if scenario.worker == "claude-acp" {
				syntheticClaudeACPBundle(t, root, bundle, acpDecisionsScript)
			}
			setup := exec.CommandContext(ctx, binary, "install", "--state-root", state, "--unit-dir", filepath.Join(root, "units"), "--bundle", bundle)
			setup.Env = env
			if out, err := setup.CombinedOutput(); err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			installedRaw, err := os.ReadFile(filepath.Join(state, "system/install.json"))
			if err != nil {
				t.Fatal(err)
			}
			var installed install.Result
			if err = json.Unmarshal(installedRaw, &installed); err != nil {
				t.Fatal(err)
			}
			h.socket = installed.Socket
			installedBinary := filepath.Join(state, "system/bin/lina")
			if scenario.worker == "claude-acp" {
				seedManagedACPDecisionFixture(t, state)
			}
			git := func(args ...string) string {
				t.Helper()
				c := exec.CommandContext(ctx, filepath.Join(bin, "git"), args...)
				c.Dir = repo
				c.Env = env
				out, e := c.CombinedOutput()
				if e != nil {
					t.Fatalf("git %v: %v %s", args, e, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-b", "dev")
			write(filepath.Join(repo, "design.md"), "# Result\nCreate result.txt.\n", 0600)
			git("add", "design.md")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
			git("remote", "add", "origin", "https://github.com/fixture/project.git")
			var requestCount atomic.Int32
			var cancelRequested atomic.Bool
			var cancelSent atomic.Bool
			var cancelArgs atomic.Value
			offered := make(chan bool, 1)
			fixtureErrors := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Tools []struct{ Name string }
					Input []json.RawMessage
				}
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
					return
				}
				if len(req.Input) > 0 {
					var item struct{ Type, Output string }
					if json.Unmarshal(req.Input[len(req.Input)-1], &item) == nil && item.Type == "function_call_output" {
						var result struct {
							Code   string
							Output json.RawMessage
						}
						if json.Unmarshal([]byte(item.Output), &result) != nil || result.Code != "ok" {
							select {
							case fixtureErrors <- fmt.Errorf("tool failed: %s", item.Output):
							default:
							}
						}
					}
				}
				for _, tool := range req.Tools {
					if tool.Name == "coding_decision" {
						var last struct{ Type string }
						if len(req.Input) > 0 {
							json.Unmarshal(req.Input[len(req.Input)-1], &last)
						}
						if last.Type == "function_call_output" {
							emitResponse(w, "Decision recorded.", true)
							return
						}
						// The manager uses the same durable decision tool as a live model. Delivery
						// is left unverified: this issue establishes receipts, never acceptance.
						decision := "delegate"
						raw, _ := json.Marshal(req.Input)
						if strings.Contains(string(raw), "coding_delivery") {
							decision = "unverified"
						}
						emitPersonaCall(w, "manager", "coding_decision", map[string]string{"decision": decision, "evidence": "Synthetic lifecycle observation."})
						return
					}
				}
				// Exercise the real on-demand declaration path before a deferred
				// dispatch or interrupt. The native declaration assertion below
				// still checks that Core actually loaded the requested tool.
				declared := func(name string) bool {
					for _, tool := range req.Tools {
						if tool.Name == name {
							return true
						}
					}
					return false
				}
				needed := ""
				if requestCount.Load() == 0 {
					needed = "dispatch_work"
				}
				if cancelRequested.Load() && !cancelSent.Load() {
					needed = "worker_interrupt"
				}
				if needed != "" && !declared(needed) {
					if !declared("load_tools") {
						select {
						case fixtureErrors <- fmt.Errorf("no declaration or loader for %s", needed):
						default:
						}
						emitResponse(w, "Worker unavailable.", true)
						return
					}
					emitPersonaCall(w, "load-"+needed, "load_tools", map[string]any{"tools": []string{needed}})
					return
				}
				if cancelRequested.Load() && cancelSent.CompareAndSwap(false, true) {
					emitPersonaCall(w, "cancel", "worker_interrupt", cancelArgs.Load())
					return
				}
				if requestCount.Add(1) == 1 {
					available := false
					for _, tool := range req.Tools {
						available = available || tool.Name == "dispatch_work"
					}
					offered <- available
					if !available {
						emitResponse(w, "Worker unavailable.", true)
						return
					}
					emitPersonaCall(w, "dispatch", "dispatch_work", map[string]any{"repository": repo, "base": "dev", "branch": "acp-result", "worker": scenario.worker, "what": "Create result.txt", "why": "Exercise ACP execution", "scope": []string{"result.txt"}, "not_touch": []string{"design.md"}, "done": "A committed result file with verified PR evidence", "design_refs": []string{"design.md#result"}, "write_area": []string{"result.txt"}})
					return
				}
				emitResponse(w, "Task accepted.", true)
			}))
			defer server.Close()
			loadfile, pidfile := filepath.Join(fixture, "loadavg"), filepath.Join(fixture, "core.pid")
			write(loadfile, "0.00 0.00 0.00 1/1 1\n", 0600)
			var crash func()
			start := func() func() {
				t.Helper()
				// Use the installed-worker load fixture so unrelated host work cannot
				// park this approval scenario. The real sampler and capacity gate run.
				// Keep a shell parent alive for bwrap's parent-death signal.
				args := []string{bwrap, "--die-with-parent", "--dev-bind", "/", "/", "--ro-bind", loadfile, "/proc/loadavg", "--", "/bin/sh", "-c", `printf '%s\n' "$$" > "$1"; shift; exec "$@"`, "acp-load", pidfile, installedBinary, "core", "--state-root", state, "--socket", h.socket}
				c := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", `"$@" & child=$!; wait "$child"`, "acp-load-parent"}, args...)...)
				c.Env = append(append([]string(nil), env...), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture", "LINA_RESPONSES_API_KEY=synthetic-core-key")
				var logs bytes.Buffer
				c.Stdout = &logs
				c.Stderr = &logs
				if err = c.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- c.Wait() }()
				stopped := false
				signalCore := func(signal syscall.Signal) error {
					raw, err := os.ReadFile(pidfile)
					if err != nil {
						return err
					}
					pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
					if err != nil || pid <= 0 {
						return fmt.Errorf("invalid Core process identity: %q", raw)
					}
					return syscall.Kill(pid, signal)
				}
				stop := func() {
					if stopped {
						return
					}
					stopped = true
					if err := signalCore(syscall.SIGTERM); err != nil {
						t.Error(err)
						c.Process.Kill()
					}
					select {
					case e := <-done:
						if e != nil {
							t.Errorf("Core exit: %v %s", e, &logs)
						}
					case <-time.After(10 * time.Second):
						c.Process.Kill()
						<-done
						t.Errorf("Core did not stop: %s", &logs)
					}
				}
				crash = func() {
					if stopped {
						return
					}
					stopped = true
					if err := signalCore(syscall.SIGKILL); err != nil {
						t.Fatal(err)
					}
					<-done
				}
				t.Cleanup(stop)
				for {
					conn, e := net.DialTimeout("unix", h.socket, 100*time.Millisecond)
					if e == nil {
						conn.Close()
						break
					}
					select {
					case e := <-done:
						stopped = true
						t.Fatalf("Core startup: %v %s", e, &logs)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(5 * time.Millisecond):
					}
				}
				return stop
			}
			stop := start()
			bootstrap, _ := conversationClient(t, h)
			person := bootstrap.Welcome.PersonID
			bootstrap.Close()
			stop()
			seedMethodologyAgents(t, h, person, "")
			stop = start()
			defer stop()
			c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
			topic := "waiting"
			if _, err = c.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Receive(ctx); err != nil {
				t.Fatal(err)
			}
			incoming := make(chan error, 1)
			approvals := make(chan protocol.ApprovalRequest, 16)
			questions := make(chan protocol.Question, 16)
			receive := func(client *clientapi.Client) {
				go func() {
					for {
						e, err := client.Receive(ctx)
						if err != nil {
							incoming <- err
							return
						}
						if e.Error != nil {
							incoming <- fmt.Errorf("Core refusal: %s", e.Error.Code)
							return
						}
						switch p := e.Payload.(type) {
						case protocol.ApprovalRequest:
							if p.Card != nil && p.Card.Outcome == nil {
								approvals <- p
							}
						case protocol.Question:
							if p.Card != nil && p.Card.Outcome == nil {
								questions <- p
							}
						}
					}
				}()
			}
			receive(c)
			key, text := "delegate", "Create result.txt through the ACP worker."
			if _, err = c.Input(ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text}); err != nil {
				t.Fatal(err)
			}
			select {
			case ok := <-offered:
				if !ok {
					t.Fatal("running Core does not offer dispatch_work with only an ACP worker installed")
				}
			case err := <-fixtureErrors:
				t.Fatal(err)
			case err := <-incoming:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal("model was not called", ctx.Err())
			}
			db, err := canon.OpenReader(ctx, filepath.Join(state, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(context.Background())
			var record dispatch.Record
			var card work.Card
			wait := func(what string, predicate func() bool) {
				t.Helper()
				for !predicate() {
					select {
					case err := <-fixtureErrors:
						t.Fatal(err)
					case err := <-incoming:
						t.Fatal(err)
					case <-ctx.Done():
						t.Fatalf("%s: %v record=%+v card=%+v", what, ctx.Err(), record, card)
					case <-time.After(5 * time.Millisecond):
					}
				}
			}
			read := func() bool {
				var raw []byte
				if db.Read(ctx, func(tx *canon.Tx) error {
					return tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch LIMIT 1").Scan(&raw)
				}) != nil {
					return false
				}
				if json.Unmarshal(raw, &record) != nil {
					return false
				}
				card, err = work.New(db).Get(ctx, record.ID)
				return err == nil
			}
			wait("dispatch started", func() bool { return read() && card.Run != nil && card.Run.ThreadID != "" })
			var approval protocol.ApprovalRequest
			var question protocol.Question
			wait("decision card", func() bool {
				select {
				case approval = <-approvals:
				case question = <-questions:
				default:
				}
				if read() && (record.State == "finished" || record.State == "unknown") {
					t.Fatalf("worker ended before decision: %+v", record)
				}
				if mode == "cancel" {
					return approval.Card != nil && question.Card != nil
				}
				return approval.Card != nil || question.Card != nil
			})
			resultPath := filepath.Join(state, "work/tasks", record.ID, "result.txt")
			noFile := func() {
				t.Helper()
				if _, e := os.Stat(resultPath); !os.IsNotExist(e) {
					t.Fatal("file written without once approval", e)
				}
			}
			noFile()
			assertACPDecisionViews(t, h, "session:"+record.Thread.ID, record.Thread.TaskID)
			if mode == "form" {
				if question.Card == nil || question.ConversationID != conversation || question.Form == nil || len(question.Form.Fields) != 3 || question.Form.PluginName != scenario.worker {
					t.Fatalf("form lost origin or schema: %+v", question)
				}
				name, count, enabled := "chosen", json.Number("3"), true
				values := protocol.FormValues{"name": {String: &name}, "count": {Number: &count}, "enabled": {Boolean: &enabled}}
				if _, err = c.AnswerQuestion(ctx, protocol.QuestionAnswer{CardID: &question.Card.ID, Revision: &question.Card.Revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}); err != nil {
					t.Fatal(err)
				}
			} else {
				if approval.Card == nil || approval.ConversationID == nil || *approval.ConversationID != conversation {
					t.Fatalf("approval lost origin: %+v", approval)
				}
				if !strings.Contains(approval.Card.Summary, scenario.worker) {
					t.Fatalf("wrong worker label: %+v", approval.Card)
				}
				if len(approval.Choices) != 2 {
					t.Fatalf("permanent choices exposed: %+v", approval.Choices)
				}
				for _, choice := range approval.Choices {
					if choice != protocol.ApprovalChoiceOnce && choice != protocol.ApprovalChoiceDeny {
						t.Fatal("unsafe choice", choice)
					}
				}
				switch mode {
				case "once", "deny":
					choice := protocol.ApprovalChoiceOnce
					if mode == "deny" {
						choice = protocol.ApprovalChoiceDeny
					}
					if _, err = c.AnswerApproval(ctx, protocol.ApprovalAnswer{CardID: &approval.Card.ID, Revision: &approval.Card.Revision, Choice: choice}); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					ctl, e := ctlfiles.Open(state)
					if e != nil {
						t.Fatal(e)
					}
					epoch, e := ctl.Epoch()
					ctl.Close()
					if e != nil {
						t.Fatal(e)
					}
					cancelArgs.Store(map[string]any{"session": card.Run.ThreadID, "turn": card.Run.TurnID, "generation": 1, "binding": epoch, "quote": "Stop this worker."})
					cancelRequested.Store(true)
					key, text = "cancel-input", "Stop this worker."
					if _, err = c.Input(ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text}); err != nil {
						t.Fatal(err)
					}
					wait("cancel delivered", func() bool { _, e := os.Stat(filepath.Join(fixture, "cancel-seen")); return e == nil })
					if !read() || !card.Active || card.Receipt != nil {
						t.Fatal("cancel ended before prompt reply")
					}
					write(filepath.Join(fixture, "release"), "", 0600)
				case "restart":
					crash()
					select {
					case <-incoming:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					c.Close()
					stop = start()
					defer stop()
					c, _ = conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
					// The same authenticated owner cannot use an old card revision.
					id, e := c.AnswerApproval(ctx, protocol.ApprovalAnswer{CardID: &approval.Card.ID, Revision: &approval.Card.Revision, Choice: protocol.ApprovalChoiceOnce})
					if e != nil {
						t.Fatal(e)
					}
					for {
						frame, e := c.Receive(ctx)
						if e != nil {
							t.Fatal(e)
						}
						if frame.ReplyTo != nil && *frame.ReplyTo == id {
							answer, ok := frame.Payload.(protocol.ApprovalAnswer)
							if frame.Error == nil && (!ok || answer.Outcome == nil || *answer.Outcome == protocol.DecisionOutcomeResolved) {
								t.Fatalf("stale approval accepted: %+v", frame)
							}
							break
						}
					}
					var saved authority.Approval
					if e := db.Read(ctx, func(tx *canon.Tx) error {
						var raw []byte
						if e := tx.QueryRowContext(ctx, "SELECT record FROM authority_approvals WHERE approval_id=?", approval.Card.ID).Scan(&raw); e != nil {
							return e
						}
						return json.Unmarshal(raw, &saved)
					}); e != nil {
						t.Fatal(e)
					}
					if saved.State != authority.Stopped {
						t.Fatalf("unused approval after restart: %+v", saved)
					}
					noFile()
					if _, e := os.Stat(filepath.Join(fixture, "answer.json")); !os.IsNotExist(e) {
						t.Fatal("answer replayed after restart", e)
					}
					return
				}
			}
			wait("worker received answer", func() bool { _, e := os.Stat(filepath.Join(fixture, "answer.json")); return e == nil })
			raw, e := os.ReadFile(filepath.Join(fixture, "answer.json"))
			if e != nil {
				t.Fatal(e)
			}
			var answer struct {
				Outcome struct{ Outcome, OptionID string }
				Action  string
				Content map[string]json.RawMessage
			}
			if json.Unmarshal(raw, &answer) != nil {
				t.Fatalf("invalid answer: %s", raw)
			}
			switch mode {
			case "once":
				if answer.Outcome.OptionID != "one-allow" {
					t.Fatalf("not allow_once: %s", raw)
				}
			case "deny":
				if answer.Outcome.OptionID != "one-deny" {
					t.Fatalf("not reject_once: %s", raw)
				}
				noFile()
			case "cancel":
				if answer.Outcome.Outcome != "cancelled" {
					t.Fatalf("not cancelled: %s", raw)
				}
				noFile()
				questionRaw, err := os.ReadFile(filepath.Join(fixture, "question-answer.json"))
				if err != nil {
					t.Fatal(err)
				}
				var questionReply struct{ Action string }
				if json.Unmarshal(questionRaw, &questionReply) != nil || questionReply.Action != "cancel" {
					t.Fatalf("pending question was not cancelled: %s", questionRaw)
				}
			case "form":
				if answer.Action != "accept" || string(answer.Content["name"]) != `"chosen"` || string(answer.Content["count"]) != "3" || string(answer.Content["enabled"]) != "true" {
					t.Fatalf("form values changed: %s", raw)
				}
			}
			wait("terminal observation", func() bool { return read() && (record.State == "finished" || record.State == "unknown") })
			if mode == "once" {
				data, e := os.ReadFile(resultPath)
				if e != nil || string(data) != "ACP approved result\n" {
					t.Fatalf("approved result: %s %v", data, e)
				}
			}
			if mode == "deny" || mode == "cancel" {
				noFile()
			}
			if mode == "cancel" && card.TurnStatus != "interrupted" {
				t.Fatalf("cancel was not interrupted: %+v", card)
			}
		})
	}
}

// Keep the form case on the built-in managed Claude profile. Its ACP program
// is synthetic, as in the other decision cases; the real pinned npm/SDK chain
// has its own TestClaudeACPThroughRunningCore acceptance. Seed a sealed cached
// program so this protocol scenario neither installs packages nor uses a PATH
// adapter that Core no longer launches.
func seedManagedACPDecisionFixture(t *testing.T, state string) {
	t.Helper()
	source := filepath.Join(state, "system/adapters/claude-acp")
	manifest := []byte(`{"name":"lina-acp-decision-fixture","version":"1.0.0","private":true}`)
	packagePath := "node_modules/@agentclientprotocol/claude-agent-acp"
	integrity := sha512.Sum512([]byte(acpDecisionsScript))
	lock, err := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{
		"":          map[string]string{"version": "1.0.0"},
		packagePath: map[string]string{"version": "1.0.0", "resolved": "https://registry.npmjs.org/@agentclientprotocol/claude-agent-acp/-/claude-agent-acp-1.0.0.tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(integrity[:])},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(source, "package.json"), manifest)
	write(filepath.Join(source, "package-lock.json"), lock)
	digest := fmt.Sprintf("%x", sha256.Sum256(append(append([]byte(nil), manifest...), lock...)))
	target := filepath.Join(source, "runtime", digest)
	files := map[string][]byte{
		packagePath + "/package.json":  []byte(`{"name":"@agentclientprotocol/claude-agent-acp","version":"1.0.0","type":"module"}`),
		packagePath + "/dist/index.js": []byte(acpDecisionsScript),
	}
	seal := map[string]string{}
	for name, body := range files {
		write(filepath.Join(target, filepath.FromSlash(name)), body)
		seal[name] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	raw, err := json.Marshal(seal)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(target, ".lina-integrity.json"), raw)
}

// Read fresh owner views while the worker is blocked on its unanswered request.
// The fixture cannot finish until the test answers, so this has no duration race.
func assertACPDecisionViews(t *testing.T, h *harness, runningRef, progressRef string) {
	t.Helper()
	for _, topic := range []string{"running", "progress"} {
		ref := runningRef
		if topic == "progress" {
			ref = progressRef
		}
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		defer cancel()
		c, err := clientapi.DialWithCapabilities(ctx, h.socket, "test", []protocol.Capability{{Name: topic, Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err = c.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		state := protocol.RunningState("")
		for state != protocol.RunningStateNeedsInput {
			frame, err := c.Receive(ctx)
			if err != nil {
				t.Fatalf("%s state for %s = %q, want needs_input: %v", topic, ref, state, err)
			}
			if frame.Error != nil {
				t.Fatalf("%s refused: %+v", topic, frame.Error)
			}
			var payloads []json.RawMessage
			if snapshot, ok := frame.Payload.(protocol.Snapshot); ok {
				if snapshot.Topic != topic {
					t.Fatalf("unexpected snapshot topic %q", snapshot.Topic)
				}
				for _, item := range snapshot.Items {
					payloads = append(payloads, item.Payload)
				}
			} else {
				raw, err := json.Marshal(frame.Payload)
				if err != nil {
					t.Fatal(err)
				}
				payloads = append(payloads, raw)
			}
			for _, raw := range payloads {
				var update struct {
					RunID string `json:"run_id"`
					State protocol.RunningState
					Rows  []protocol.ProgressRow
				}
				if err := json.Unmarshal(raw, &update); err != nil {
					t.Fatal(err)
				}
				if topic == "running" && update.RunID == ref {
					state = update.State
				}
				for _, row := range update.Rows {
					if topic == "progress" && row.Ref == ref && row.Line.State != nil {
						state = *row.Line.State
					}
				}
			}
		}
	}
}

const acpDecisionsScript = `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
const root=process.env.LINA_ACP_FIXTURE, mode=process.env.LINA_ACP_CASE;
if(path.basename(process.argv[1])==='gh'){console.log('[]');process.exit(0)}
if(process.argv[2]==='--version'){console.log('fixture-unverified-9');process.exit(0)}
if(mode==='form'&&(path.basename(process.argv[1])!=='index.js'||!process.env.CLAUDE_CODE_EXECUTABLE))throw Error('managed Claude ACP fixture not selected');
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x}));
const reply=(id,result)=>send({id,result});
let prompt;
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line);
 switch(m.method){
 case 'initialize':reply(m.id,{protocolVersion:1,agentInfo:{name:'fixture',version:'fixture-unverified-9'}});break;
 case 'session/new':reply(m.id,{sessionId:'acp-fixture'});break;
 case 'session/prompt':
  prompt=m.id;
  if(mode==='form'||mode==='cancel')send({id:42,method:'elicitation/create',params:{sessionId:'acp-fixture',mode:'form',message:'Choose result settings',requestedSchema:{type:'object',properties:{name:{type:'string'},count:{type:'integer',minimum:1},enabled:{type:'boolean'}},required:['name','count','enabled']}}});
  if(mode!=='form')send({id:'permission',method:'session/request_permission',params:{sessionId:'acp-fixture',toolCall:{toolCallId:'write-result',title:'Write result.txt',rawInput:{path:'result.txt'}},options:[{kind:'allow_always',optionId:'persist-allow',name:'Always'},{kind:'reject_always',optionId:'persist-deny',name:'Never'},{kind:'allow_once',optionId:'one-allow',name:'Once'},{kind:'reject_once',optionId:'one-deny',name:'Deny'}]}});
  break;
 case 'session/cancel':
  if(!fs.existsSync(path.join(root,'answer.json')))throw Error('cancel without pending answer');
  fs.writeFileSync(path.join(root,'cancel-seen'),'1');
  const timer=setInterval(()=>{if(fs.existsSync(path.join(root,'release'))){clearInterval(timer);reply(prompt,{stopReason:'cancelled'})}},5);break;
 case undefined:
  if(m.error)throw Error('request rejected: '+JSON.stringify(m.error));
  fs.writeFileSync(path.join(root,m.id===42&&mode==='cancel'?'question-answer.json':'answer.json'),JSON.stringify(m.result));
  if(m.result.outcome?.optionId==='persist-allow'||m.result.outcome?.optionId==='persist-deny')throw Error('permanent choice');
  if(m.result.outcome?.outcome==='cancelled'||m.result.action==='cancel')break;
  if(m.result.outcome?.optionId==='one-allow')fs.writeFileSync('result.txt','ACP approved result\n');
  reply(prompt,{stopReason:'end_turn'});break;
 default:throw Error('unexpected method '+m.method);
 }
});
`
