// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
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

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

// This scenario launches the real Core command. Only the model, ACP agent and
// GitHub boundary are synthetic; dispatch, worktrees, commits and cards are real.
func TestCursorReadinessThroughRunningCore(t *testing.T) {
	runACPReadinessThroughRunningCore(t, acpReadinessScenario{
		worker: "cursor", binary: "cursor-agent", version: "cursor-fixture-210",
		refusedVersion: "cursor-fixture-210", versionProbe: true,
		fallback: "gemini", fallbackVersion: "gemini-fixture-210", script: cursorAgentScript,
	})
}

type acpReadinessScenario struct {
	worker, binary, version, refusedVersion string
	fallback, fallbackVersion, script       string
	versionProbe                            bool
}

func runACPReadinessThroughRunningCore(t *testing.T, scenario acpReadinessScenario) {
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
	for _, mode := range []string{"signed-in", "needs-sign-in"} {
		t.Run(mode, func(t *testing.T) {
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
			if mode == "needs-sign-in" {
				names = append(names, scenario.fallback)
			}
			for _, name := range names {
				write(filepath.Join(bin, name), scenario.script, 0700)
			}
			env := []string{"PATH=" + bin, "HOME=" + root, "XDG_CONFIG_HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C", "LINA_ACP_FIXTURE=" + fixture, "LINA_ACP_CASE=" + mode}
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
			versionSeen := false
			for _, tool := range installed.Tools {
				if tool.Name == scenario.binary {
					versionSeen = tool.Path == filepath.Join(bin, scenario.binary) && tool.Version == scenario.version && tool.Status == "unverified"
				}
			}
			if !versionSeen {
				t.Fatal("worker version was not recorded as unverified", installed.Tools)
			}
			installTrace, err := os.ReadFile(filepath.Join(fixture, "trace"))
			if err != nil {
				t.Fatal(err)
			}
			probe := "initialize"
			if scenario.versionProbe {
				probe = "--version"
			}
			var primaryProbes []string
			for line := range strings.SplitSeq(strings.TrimSpace(string(installTrace)), "\n") {
				if strings.HasPrefix(line, scenario.binary+" ") {
					primaryProbes = append(primaryProbes, line)
				}
			}
			if len(primaryProbes) != 1 || primaryProbes[0] != scenario.binary+" "+probe {
				t.Fatal("installation performed more than a version probe", primaryProbes)
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
			var targetRepo atomic.Value
			targetRepo.Store(repo)
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
				// Work tools are deferred behind load_tools (tool groups, #758).
				// Load dispatch_work first, as the ACP worker scenario does.
				if requestCount.Load() == 0 {
					declared := func(name string) bool {
						for _, tool := range req.Tools {
							if tool.Name == name {
								return true
							}
						}
						return false
					}
					if !declared("dispatch_work") && declared("load_tools") {
						emitPersonaCall(w, "load-dispatch_work", "load_tools", map[string]any{"tools": []string{"dispatch_work"}})
						return
					}
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
					emitPersonaCall(w, "dispatch", "dispatch_work", map[string]any{"repository": targetRepo.Load().(string), "base": "dev", "branch": "acp-result", "what": "Create result.txt", "why": "Exercise ACP worker readiness", "scope": []string{"result.txt"}, "not_touch": []string{"design.md"}, "done": "A committed result file with verified PR evidence", "design_refs": []string{"design.md#result"}, "write_area": []string{"result.txt"}})
					return
				}
				emitResponse(w, "Task accepted.", true)
			}))
			defer server.Close()
			loadfile, pidfile := filepath.Join(fixture, "loadavg"), filepath.Join(fixture, "core.pid")
			write(loadfile, "0.00 0.00 0.00 1/1 1\n", 0600)
			start := func() func() {
				t.Helper()
				// Match the existing installed-worker load fixture: only this Core
				// sees fixed load; the real sampler, capacity gate and binary run.
				// A shell parent keeps bwrap's parent-death signal tied to a live
				// thread. The inner shell records Core's PID for graceful shutdown.
				args := []string{bwrap, "--die-with-parent", "--dev-bind", "/", "/", "--ro-bind", loadfile, "/proc/loadavg", "--", "/bin/sh", "-c", `printf '%s\n' "$$" > "$1"; shift; exec "$@"`, "cursor-load", pidfile, installedBinary, "core", "--state-root", state, "--socket", h.socket}
				c := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", `"$@" & child=$!; wait "$child"`, "cursor-load-parent"}, args...)...)
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
					rawPID, pidErr := os.ReadFile(pidfile)
					pid, parseErr := strconv.Atoi(strings.TrimSpace(string(rawPID)))
					if pidErr != nil || parseErr != nil || pid <= 0 {
						t.Error("missing Core process identity", pidErr, parseErr)
						c.Process.Kill()
					} else {
						syscall.Kill(pid, syscall.SIGTERM)
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
			defer func() { stop() }()
			phases := 1
			if mode == "needs-sign-in" {
				phases = 2
			}
			for phase := range phases {
				if phase == 1 {
					// Sign-in happens outside Core. The same installed worker must
					// become usable for a new task after durable state is reopened.
					// Use another repository because the first result still awaits
					// acceptance and correctly retains its write-area reservation.
					stop()
					write(filepath.Join(fixture, "signed-in"), "", 0600)
					if err := os.Remove(filepath.Join(fixture, "release")); err != nil {
						t.Fatal(err)
					}
					nextRepo := filepath.Join(root, "next-repo")
					git("clone", repo, nextRepo)
					git("-C", nextRepo, "remote", "set-url", "origin", "https://github.com/fixture/recovered.git")
					targetRepo.Store(nextRepo)
					requestCount.Store(0)
					stop = start()
				}
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
				key, text := fmt.Sprintf("delegate-%d", phase), "Create result.txt through the ACP worker."
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
						return tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE repository=?", targetRepo.Load().(string)).Scan(&raw)
					}) != nil {
						return false
					}
					if json.Unmarshal(raw, &record) != nil {
						return false
					}
					if bytes.Contains(raw, []byte("account-detail-must-stay-private")) {
						t.Fatal("remote account detail reached the dispatch record")
					}
					card, err = work.New(db).Get(ctx, record.ID)
					return err == nil
				}
				wait("progress before finish", func() bool {
					if read() && (record.State == "unknown" || record.State == "finished") {
						t.Fatalf("readiness did not reach a working session: state=%s code=%s tried=%v", record.State, record.Code, record.Tried)
					}
					return read() && card.Active && card.Receipt == nil && len(card.Stages) > 0 && card.Stages[0].Source == "worker_report" && strings.Contains(card.Stages[0].Summary, "Working on result.txt")
				})
				progressLedger, err := work.New(db).Ledger(ctx, record.ID)
				if err != nil {
					t.Fatal(err)
				}
				versionReported := false
				wantWorker, wantVersion := scenario.worker, scenario.version
				if mode == "needs-sign-in" && phase == 0 {
					wantWorker, wantVersion = scenario.fallback, scenario.fallbackVersion
				}
				for _, observation := range progressLedger {
					versionReported = versionReported || observation.Kind == "worker_progress" && strings.Contains(observation.Value, wantVersion) && strings.Contains(observation.Value, "unverified")
				}
				if !versionReported {
					t.Fatal("unverified version was not reported on the work card")
				}
				if mode == "needs-sign-in" && phase == 0 {
					if len(record.ReadinessFailures) != 1 || record.ReadinessFailures[0].Worker != scenario.worker || record.ReadinessFailures[0].Code != "needs_sign_in" {
						t.Fatal("worker readiness failure missing", record.ReadinessFailures)
					}
					if scenario.refusedVersion != "" && record.ReadinessFailures[0].Version != scenario.refusedVersion {
						t.Fatal("worker readiness version missing", record.ReadinessFailures)
					}
					signInReported := false
					for _, observation := range progressLedger {
						signInReported = signInReported || observation.Kind == "worker_progress" && strings.Contains(observation.Value, scenario.worker) && strings.Contains(observation.Value, "needs sign-in")
					}
					if !signInReported {
						t.Fatal("worker needs sign-in was not reported", progressLedger)
					}
					if strings.Join(record.Tried, ",") != scenario.worker+","+scenario.fallback {
						t.Fatal("wrong fallback attempts", record.Tried)
					}
				}
				if phase == 1 && (strings.Join(record.Tried, ",") != scenario.worker || len(record.ReadinessFailures) != 0) {
					t.Fatal("signed-in worker did not recover for the new task", record)
				}
				if record.Worker != wantWorker || record.Version != wantVersion {
					t.Fatalf("worker/version: %+v", record)
				}
				trace, err := os.ReadFile(filepath.Join(fixture, "trace"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(trace), scenario.binary+" --version") != scenario.versionProbe || !strings.Contains(string(trace), scenario.binary+" initialize") || !strings.Contains(string(trace), scenario.binary+" session/new") {
					t.Fatal("incorrect worker probes", string(trace))
				}
				if mode == "needs-sign-in" && phase == 0 && strings.Contains(string(trace), scenario.binary+" session/prompt") {
					t.Fatal("prompt sent without sign-in")
				}
				write(filepath.Join(fixture, "release"), "", 0600)
				wait("terminal observation", func() bool { return read() && (record.State == "finished" || record.State == "unknown") })
				if card.Receipt == nil || card.Receipt.Outcome != "ready_for_review" || len(card.Receipt.Provenance) != 1 || !strings.HasPrefix(card.Receipt.Provenance[0].ArtifactID, "adapter-derived:") || record.PR == nil {
					t.Fatal("adapter receipt missing", record, card)
				}
				b, e := os.ReadFile(filepath.Join(state, "work/tasks", record.ID, "result.txt"))
				if e != nil || string(b) != "ACP result\n" {
					t.Fatalf("result: %q %v", b, e)
				}
				c.Close()
				<-incoming
			}
		})
	}
}

const cursorAgentScript = `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import {execFileSync} from 'node:child_process';
const root=process.env.LINA_ACP_FIXTURE, name=path.basename(process.argv[1]);
if (name==='gh') {
 if(process.argv[2]==='api') console.log('[[]]');
 else console.log(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
 process.exit(0);
}
if(process.env.LINA_RESPONSES_API_KEY) throw Error('Core credential leaked');
const trace=method=>fs.appendFileSync(path.join(root,'trace'),name+' '+method+'\n');
if(process.argv[2]==='--version') { trace('--version'); console.log('cursor-fixture-210'); process.exit(0); }
if(JSON.stringify(process.argv.slice(2))!==JSON.stringify(name==='cursor-agent'?['acp']:['--acp'])) throw Error('unexpected arguments');
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x}));
const reply=(id,result)=>send({id,result});
const session=name+'-session';
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line);trace(m.method);
 switch(m.method){
 case 'initialize':
  if(m.params.protocolVersion!==1||m.params.clientCapabilities.fs||m.params.clientCapabilities.terminal)throw Error('unexpected capabilities');
  reply(m.id,{protocolVersion:1,agentCapabilities:{loadSession:true},...(name==='cursor-agent'?{}:{agentInfo:{name:'gemini',version:'gemini-fixture-210'}})});break;
 case 'session/new':
  if(m.params.cwd!==process.cwd()||m.params.mcpServers.length!==1||m.params.mcpServers[0].name!=='lina'||m.params.mcpServers[0].args[0]!=='worker-tools')throw Error('unexpected session setup');
  if(name==='cursor-agent'&&process.env.LINA_ACP_CASE==='needs-sign-in'&&!fs.existsSync(path.join(root,'signed-in'))) {
   send({id:m.id,error:{code:-32000,message:'Authentication required',data:{private:'account-detail-must-stay-private'}}});break;
  }
  reply(m.id,{sessionId:session});break;
 case 'session/prompt': {
  if(name==='cursor-agent'&&process.env.LINA_ACP_CASE==='needs-sign-in'&&!fs.existsSync(path.join(root,'signed-in')))throw Error('prompt without sign-in');
  const packet=m.params.prompt[0].text;
  send({method:'session/update',params:{sessionId:session,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Working on result.txt'}}}});
  const timer=setInterval(()=>{
   if(!fs.existsSync(path.join(root,'release')))return;
   clearInterval(timer);
   const definition=JSON.parse(packet.slice(packet.indexOf('{')));
   const git=(...args)=>execFileSync('git',args,{encoding:'utf8'}).trim();
   git('switch','-c',definition.branch);fs.writeFileSync('result.txt','ACP result\n');git('add','result.txt');git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Create result');
   const head=git('rev-parse','HEAD');
   fs.writeFileSync(path.join(root,'pr.json'),JSON.stringify({number:17,url:git('remote','get-url','origin').replace(/\.git$/,'')+'/pull/17',state:'OPEN',isDraft:false,headRefName:definition.branch,baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:'COMPLETED',conclusion:'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
   reply(m.id,{stopReason:'end_turn'});
  },5);break;
 }
 default:throw Error('unexpected method '+m.method);
 }
});
`
