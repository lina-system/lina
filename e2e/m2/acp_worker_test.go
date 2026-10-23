// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

// This scenario launches the real Core command. Only the model, ACP agent and
// GitHub boundary are synthetic; dispatch, worktrees, commits and cards are real.
func TestACPWorkersThroughRunningCore(t *testing.T) {
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
		{"success", "gemini", "gemini", "success"},
		{"cursor", "cursor", "cursor-agent", "success"},
		{"grok", "grok", "grok", "success"},
		{"claude-acp", "claude-acp", "claude-agent-acp", "success"},
		{"exit", "gemini", "gemini", "exit"},
		{"cancel", "gemini", "gemini", "cancel"},
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
				write(filepath.Join(bin, name), acpAgentScript, 0700)
			}
			env := []string{"PATH=" + bin, "HOME=" + root, "XDG_CONFIG_HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C", "LINA_ACP_FIXTURE=" + fixture, "LINA_ACP_CASE=" + mode, "LINA_ACP_WORKER=" + scenario.worker}
			write(filepath.Join(bin, "systemctl"), "#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\ncase \"$2\" in show) printf 'not-found\\n';; daemon-reload|enable) exit 0;; *) exit 97;; esac\n", 0700)
			bundle := makeInstallBundle(t, root, binary, nodeBinary)
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
			start := func() func() {
				t.Helper()
				c := exec.CommandContext(ctx, installedBinary, "core", "--state-root", state, "--socket", h.socket)
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
				stop := func() {
					if stopped {
						return
					}
					stopped = true
					c.Process.Signal(syscall.SIGTERM)
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
			c, conversation := conversationClient(t, h)
			incoming := make(chan error, 1)
			go func() {
				for {
					e, err := c.Receive(ctx)
					if err != nil {
						incoming <- err
						return
					}
					if state, ok := e.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active && requestCount.Load() == 0 {
						incoming <- fmt.Errorf("Core ended the turn before dispatching to the model fixture")
						return
					}

					if e.Error != nil {
						incoming <- fmt.Errorf("Core refusal: %s", e.Error.Code)
						return
					}
				}
			}()
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
			// The worker waits on our barrier. Seeing a durable stage here proves the
			// card changed before the prompt reply rather than after completion.
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
			wait("progress before finish", func() bool {
				return read() && card.Active && card.Receipt == nil && len(card.Stages) > 0 && card.Stages[0].Source == "worker_report" && strings.Contains(card.Stages[0].Summary, "Working on result.txt")
			})
			progressLedger, err := work.New(db).Ledger(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			versionReported := false
			for _, observation := range progressLedger {
				versionReported = versionReported || observation.Kind == "worker_progress" && strings.Contains(observation.Value, "fixture-unverified-9") && strings.Contains(observation.Value, "unverified")
			}
			if !versionReported {
				t.Fatal("unverified version was not reported on the work card")
			}
			if record.Version != "fixture-unverified-9" {
				t.Fatalf("readiness version not recorded: %q", record.Version)
			}
			if _, err = os.Stat(filepath.Join(fixture, "credential-filtered")); err != nil {
				t.Fatal("worker credential boundary not observed", err)
			}
			if mode == "cancel" {
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
				wait("cancel reached agent", func() bool { _, e := os.Stat(filepath.Join(fixture, "cancel-seen")); return e == nil })
				if !read() || !card.Active || card.Receipt != nil {
					t.Fatal("Core finalized cancel before reply")
				}
			}
			write(filepath.Join(fixture, "release"), "", 0600)
			wait("terminal observation", func() bool { return read() && (record.State == "finished" || record.State == "unknown") })
			switch mode {
			case "exit":
				if record.State != "unknown" || record.Code != "prompt_failed" || card.Receipt != nil {
					t.Fatal("open prompt exit became done", record, card)
				}
			case "cancel":
				if card.TurnStatus != "interrupted" || card.Receipt == nil || card.Receipt.Outcome != "interrupted" {
					t.Fatal("cancelled reply not recorded as interrupted", record, card)
				}
			case "success":
				if card.Receipt == nil || card.Receipt.Outcome != "ready_for_review" || len(card.Receipt.Provenance) != 1 || !strings.HasPrefix(card.Receipt.Provenance[0].ArtifactID, "adapter-derived:") || record.PR == nil {
					t.Fatal("adapter receipt missing", record, card)
				}
				b, e := os.ReadFile(filepath.Join(state, "work/tasks", record.ID, "result.txt"))
				if e != nil || string(b) != "ACP result\n" {
					t.Fatalf("result: %q %v", b, e)
				}
			}
		})
	}
}

type harness struct {
	ctx          context.Context
	root, socket string
}

const acpAgentScript = `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import {execFileSync} from 'node:child_process';
const root=process.env.LINA_ACP_FIXTURE;
if (path.basename(process.argv[1])==='gh') {
 if (process.argv[2]==='api') console.log('[[]]');
 else console.log(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
 process.exit(0);
}
if (process.env.LINA_RESPONSES_API_KEY) throw Error('Core credential leaked');
if (process.argv[2]==='--version') { console.log('fixture-unverified-9'); process.exit(0); }
const worker=process.env.LINA_ACP_WORKER;
const args={gemini:['--acp'],cursor:['acp'],grok:['--no-auto-update','agent','stdio'],'claude-acp':[]}[worker];
if(JSON.stringify(process.argv.slice(2))!==JSON.stringify(args))throw Error('unexpected worker arguments');
if(worker==='claude-acp'&&process.env.CLAUDE_CODE_EXECUTABLE!==path.join(path.dirname(process.argv[1]),'claude'))throw Error('installed Claude executable not selected');
fs.writeFileSync(path.join(root,'credential-filtered'),'1');
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x}));
const reply=(id,result)=>send({id,result});
const progress=text=>send({method:'session/update',params:{sessionId:'acp-fixture',update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text}}}});
let pending,packet;
const release=fn=>{const timer=setInterval(()=>{if(fs.existsSync(path.join(root,'release'))){clearInterval(timer);fn()}},5)};
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line);
 switch(m.method){
 case 'initialize':
  if(m.params.protocolVersion!==1||m.params.clientCapabilities.fs||m.params.clientCapabilities.terminal)throw Error('unexpected capabilities');
  reply(m.id,{protocolVersion:1,...(worker==='cursor'||worker==='grok'?{}:{agentInfo:{name:'fixture',version:worker==='claude-acp'?'adapter-fixture-1':'fixture-unverified-9'}})});break;
 case 'session/new':
  if(m.params.cwd!==process.cwd()||m.params.mcpServers.length!==0)throw Error('unexpected session setup');
  fs.writeFileSync(path.join(root,'cwd'),process.cwd());reply(m.id,{sessionId:'acp-fixture'});break;
 case 'session/prompt':
  pending=m.id;packet=m.params.prompt[0].text;progress('Working on result.txt');
  if(process.env.LINA_ACP_CASE==='cancel')break;
  release(()=>{
   if(process.env.LINA_ACP_CASE==='exit')process.exit(0);
   const definition=JSON.parse(packet.slice(packet.indexOf('{')));
   const git=(...args)=>execFileSync('git',args,{encoding:'utf8'}).trim();
   git('switch','-c',definition.branch);fs.writeFileSync('result.txt','ACP result\n');git('add','result.txt');git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Create ACP result');
   const head=git('rev-parse','HEAD');
   fs.writeFileSync(path.join(root,'pr.json'),JSON.stringify({number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:definition.branch,baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:'COMPLETED',conclusion:'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
   reply(pending,{stopReason:'end_turn'});
  });break;
 case 'session/cancel':fs.writeFileSync(path.join(root,'cancel-seen'),'1');progress('Cancel received');release(()=>reply(pending,{stopReason:'cancelled'}));break;
 default:throw Error('unexpected method '+m.method);
 }
});
`

func conversationClient(t *testing.T, h *harness, extra ...protocol.Capability) (*clientapi.Client, string) {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", append([]protocol.Capability{{Name: "conversation", Version: "1.0"}}, extra...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err = c.History(h.ctx, protocol.HistoryGet{}); err != nil {
		t.Fatal(err)
	}
	e, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, ok := e.Payload.(protocol.History)
	if !ok || history.ConversationID == nil {
		t.Fatal("no conversation")
	}
	topic := "conversation:" + *history.ConversationID
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := c.Receive(h.ctx); err != nil || snapshot.Kind != protocol.KindSnapshot {
		t.Fatal("no history snapshot")
	}
	// Empty input probes admission without creating a message. Only recovery's
	// explicit refusal permits another probe.
	for {
		if _, err = c.Input(h.ctx, protocol.Input{}); err != nil {
			t.Fatal(err)
		}
		ready, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ready.Error != nil && ready.Error.Code == protocol.ErrorMalformed {
			break
		}
		if ready.Error == nil || ready.Error.Code != protocol.ErrorUnavailable {
			t.Fatal("unexpected readiness refusal")
		}
	}
	return c, *history.ConversationID
}

func emitResponse(w http.ResponseWriter, text string, complete bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", text)
	if complete {
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}
}

func emitPersonaCall(w http.ResponseWriter, id, name string, args any) {
	raw, _ := json.Marshal(args)
	item, _ := json.Marshal(map[string]any{"type": "function_call", "id": "item-" + id, "call_id": id, "name": name, "arguments": string(raw)})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}

func seedMethodologyAgents(t *testing.T, h *harness, person, selected string) map[string]string {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	s, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RegisterMethodologies(h.ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, a := range rows {
		ids[a.Preset] = a.ID
	}
	for _, m := range agents.Methodologies() {
		if ids[m.Preset] != "" {
			continue
		}
		a, err := s.Create(h.ctx, agents.Create{PersonID: person, Name: m.Name, Preset: m.Preset})
		if err != nil {
			t.Fatal(err)
		}
		ids[m.Preset] = a.ID
	}
	if selected != "" {
		if err = db.Write(h.ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(h.ctx, "UPDATE agent_first_run SET agent_id=? WHERE singleton=1", ids[selected])
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

type methodologyRequest struct {
	Instructions string
	Tools        []struct{ Name string }
	Input        []json.RawMessage
}

func makeInstallBundle(t *testing.T, base, lina, node string) string {
	t.Helper()
	bundle := filepath.Join(base, "bundle")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	binaries := []map[string]any{}
	for name, source := range map[string]string{"lina": lina, "lina-node": node} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(bundle, name), data, 0700); err != nil {
			t.Fatal(err)
		}
		binaries = append(binaries, map[string]any{"name": name, "size": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	// Synthetic provenance, exact hashes of the artifacts built by this scenario.
	m := map[string]any{"version": "0.0.1-alpha.dev", "commit": strings.Repeat("a", 40), "date": "2026-10-20", "goToolchain": "go1.27.3", "kitVersion": 1,
		"supportedCombinations": manifest.ClientCombinations(),
		"target":                runtime.GOOS + "/" + runtime.GOARCH,
		"providers":             map[string]any{"chatgpt": map[string]any{"adapter": "responses", "retryCount": 0, "requestFields": []string{"model", "input", "store", "stream"}}},
		"sqlite":                map[string]any{"module": "github.com/ncruces/go-sqlite3", "version": "v0.30.5", "moduleSum": "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=", "sqliteVersion": "3.51.2", "wasmSHA256": strings.Repeat("d", 64), "execution": "wazero", "sqliteVec": nil}, "binaries": binaries}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bundle, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return bundle
}
