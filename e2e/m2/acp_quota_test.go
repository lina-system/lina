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

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// This scenario launches the real Core command. Only the model, ACP agent and
// GitHub boundary are synthetic; dispatch, worktrees, commits and cards are real.
func TestACPTypedFailuresThroughRunningCore(t *testing.T) {
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
	for _, mode := range []string{"needs-sign-in", "rate-limited", "quota-exhausted", "double-stop", "native-unresolved", "native-completed", "vendor", "vendor-missing", "silent"} {
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
			for _, name := range []string{"claude", "gemini", "grok", "gh"} {
				write(filepath.Join(bin, name), quotaAgentScript, 0700)
			}
			env := []string{"PATH=" + bin, "HOME=" + root, "XDG_CONFIG_HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C", "LINA_ACP_FIXTURE=" + fixture, "LINA_ACP_CASE=" + mode}
			write(filepath.Join(bin, "systemctl"), "#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\ncase \"$2\" in show) printf 'not-found\\n';; daemon-reload|enable) exit 0;; *) exit 97;; esac\n", 0700)
			bundle := makeInstallBundle(t, root, binary, nodeBinary)
			syntheticClaudeACPBundle(t, root, bundle, quotaAgentScript)
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
					emitPersonaCall(w, "dispatch", "dispatch_work", map[string]any{"repository": repo, "base": "dev", "branch": "acp-result", "what": "Create result.txt", "why": "Exercise typed ACP failures and quota", "scope": []string{"result.txt"}, "not_touch": []string{"design.md"}, "done": "A committed result file with verified PR evidence", "design_refs": []string{"design.md#result"}, "write_area": []string{"result.txt"}})
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
			defer stop()
			c, conversation := conversationClient(t, h)
			defer c.Close()
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
				if bytes.Contains(raw, []byte("account-detail-must-stay-private")) {
					t.Fatal("remote account detail reached the dispatch record")
				}
				card, err = work.New(db).Get(ctx, record.ID)
				return err == nil
			}
			if mode != "double-stop" && mode != "native-unresolved" && mode != "native-completed" {
				wait("replacement or unknown worker progress", func() bool {
					if read() && (record.State == "finished" || record.State == "unknown") {
						t.Fatalf("no working session: %+v", record)
					}
					return read() && card.Active && len(card.Stages) > 0 && strings.Contains(card.Stages[0].Summary, "Working on result.txt")
				})
			}
			write(filepath.Join(fixture, "release"), "", 0600)
			wait("terminal observation", func() bool { return read() && (record.State == "finished" || record.State == "unknown") })
			wantTried := "claude-acp"
			if mode == "needs-sign-in" || mode == "rate-limited" || mode == "quota-exhausted" || mode == "double-stop" {
				wantTried += ",gemini"
			}
			if strings.Join(record.Tried, ",") != wantTried {
				t.Fatalf("attempts: %v", record.Tried)
			}
			if mode == "needs-sign-in" {
				if len(record.ReadinessFailures) != 1 || record.ReadinessFailures[0].Worker != "claude-acp" || record.ReadinessFailures[0].Code != "needs_sign_in" {
					t.Fatalf("readiness: %+v", record.ReadinessFailures)
				}
			}
			if mode == "native-unresolved" || mode == "native-completed" {
				if record.State != "unknown" || record.Attempt != 1 || record.Snapshot == nil || record.Snapshot.EffectsKnown {
					t.Fatalf("unresolved native effect allowed replacement: %+v", record)
				}
			} else if mode == "double-stop" {
				if card.Receipt == nil || card.Receipt.Outcome != "failed" || record.Attempt != 2 {
					t.Fatalf("second stop did not settle: %+v %+v", record, card)
				}
			} else {
				if card.Receipt == nil || card.Receipt.Outcome != "ready_for_review" || record.PR == nil {
					t.Fatalf("result receipt missing: %+v %+v", record, card)
				}
				b, e := os.ReadFile(filepath.Join(state, "work/tasks", record.ID, "result.txt"))
				if e != nil || string(b) != "ACP result\n" {
					t.Fatalf("result: %q %v", b, e)
				}
			}
			var packets []struct {
				Worker, Packet, Cwd string
				PID                 int
			}
			raw, e := os.ReadFile(filepath.Join(fixture, "packets"))
			if e != nil {
				t.Fatal(e)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var p struct {
					Worker, Packet, Cwd string
					PID                 int
				}
				if err := json.Unmarshal([]byte(line), &p); err != nil {
					t.Fatal(err)
				}
				if p.Packet != record.Packet || p.Cwd != filepath.Join(state, "work/tasks", record.ID) {
					t.Fatal("replacement changed persisted task packet or worktree")
				}
				packets = append(packets, p)
			}
			wantPrompts := 1
			if mode == "rate-limited" || mode == "quota-exhausted" || mode == "double-stop" {
				wantPrompts = 2
			}
			if len(packets) != wantPrompts {
				t.Fatalf("prompt count: %d want %d", len(packets), wantPrompts)
			}
			var rows []provider.QuotaRow
			if err := db.Read(ctx, func(tx *canon.Tx) error {
				var raw []byte
				err := tx.QueryRowContext(ctx, "SELECT rows FROM worker_quota WHERE worker=?", "claude-acp").Scan(&raw)
				if mode == "silent" || mode == "needs-sign-in" {
					return nil
				}
				if err != nil {
					return err
				}
				return json.Unmarshal(raw, &rows)
			}); err != nil {
				t.Fatal(err)
			}
			if mode == "silent" || mode == "needs-sign-in" {
				var count int
				if err := db.Read(ctx, func(tx *canon.Tx) error {
					return tx.QueryRowContext(ctx, "SELECT count(*) FROM worker_quota WHERE worker=?", "claude-acp").Scan(&count)
				}); err != nil || count != 0 {
					t.Fatalf("silent worker quota rows: %d %v", count, err)
				}
				if mode == "silent" && record.Worker != "claude-acp" {
					t.Fatal("unknown worker excluded")
				}
			} else {
				if len(rows) != 1 {
					t.Fatalf("quota rows: %+v", rows)
				}
				q := rows[0]
				if mode == "vendor" {
					if q.Window != provider.FiveHour || q.State != provider.QuotaWarning || q.ResetsAt == nil || !q.ResetsAt.Equal(time.Unix(2000000000, 0)) || q.UsedPct == nil || *q.UsedPct != 85 {
						t.Fatalf("reported quota: %+v", q)
					}
				} else if mode == "vendor-missing" {
					if q.Window != provider.UnknownWindow || q.State != provider.QuotaOK || q.ResetsAt != nil || q.UsedPct != nil {
						t.Fatalf("invented quota: %+v", q)
					}
				} else if q.Window != provider.UnknownWindow || q.State != provider.QuotaBlocked || q.ResetsAt != nil {
					t.Fatalf("parked quota: %+v", q)
				}
			}
			// Terminal persistence and joined process shutdown form the absence
			// barrier: no third prompt can be emitted after this observation.
			stop()
			c.Close()
			<-incoming
			raw, e = os.ReadFile(filepath.Join(fixture, "packets"))
			if e != nil {
				t.Fatal(e)
			}
			if len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != wantPrompts {
				t.Fatal("extra prompt after terminal result")
			}

		})
	}
}

const quotaAgentScript = `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import {execFileSync} from 'node:child_process';
const root=process.env.LINA_ACP_FIXTURE, entry=path.basename(process.argv[1]), name=entry==='index.js'?'claude-agent-acp':entry, mode=process.env.LINA_ACP_CASE;
if(name==='gh') {
 if(process.argv[2]==='api') console.log('[[]]');
 else console.log(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
 process.exit(0);
}
if(process.env.LINA_RESPONSES_API_KEY)throw Error('Core credential leaked');
if(process.argv[2]==='--version'){console.log('quota-fixture-750');process.exit(0)}
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x})),reply=(id,result)=>send({id,result});
const session=name+'-session';
const meta={jetbrains:{air:{version:1,capabilities:['sessionFailure']}}};
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line);
 const update=u=>send({method:'session/update',params:{sessionId:session,update:u}});
 switch(m.method){
 case 'initialize':
  if(!m.params.clientCapabilities?._meta?.jetbrains?.air?.capabilities?.includes('sessionFailure'))throw Error('session failure not negotiated');
  reply(m.id,{protocolVersion:1,_meta:meta,agentInfo:{name,version:'quota-fixture-750'}});break;
 case 'session/new':
  if(name==='claude-agent-acp'&&mode==='needs-sign-in'){send({id:m.id,error:{code:-32000,message:'account-detail-must-stay-private'}});break}
  reply(m.id,{sessionId:session});break;
 case 'session/prompt':{
  const packet=m.params.prompt[0].text;
  const prior=path.join(root,'previous-pid');
  if(fs.existsSync(prior)){
   try{process.kill(Number(fs.readFileSync(prior,'utf8')),0);throw Error('previous worker still alive')}
   catch(e){if(e.code!=='ESRCH')throw e}
  }
  fs.appendFileSync(path.join(root,'packets'),JSON.stringify({Worker:name,Packet:packet,Cwd:process.cwd(),PID:process.pid})+'\n');
  const first=name==='claude-agent-acp';
  if((first&&['rate-limited','quota-exhausted','double-stop','native-unresolved','native-completed'].includes(mode))||(name==='gemini'&&mode==='double-stop')){
   fs.writeFileSync(prior,String(process.pid));
   if(mode==='native-unresolved'||mode==='native-completed'){
    update({sessionUpdate:'tool_call',toolCallId:'external-write',title:'Write',status:'in_progress'});
    if(mode==='native-completed')update({sessionUpdate:'tool_call_update',toolCallId:'external-write',status:'completed'});
   }
   const actions=mode==='rate-limited'?['retry']:[];
   const failure={sessionUpdate:'session_info_update',_meta:{jetbrains:{air:{version:1,sessionFailure:{id:'incident',revision:1,category:'limit',severity:'error',actions,title:'account-detail-must-stay-private'}}}}};
   update(failure);update(failure); // duplicate reports must not repeat the task
   reply(m.id,{stopReason:'end_turn'});break;
  }
  if(first&&mode.startsWith('vendor')){
   const limit=mode==='vendor'?{status:'allowed_warning',rateLimitType:'five_hour',utilization:0.85,resetsAt:2000000000}:{status:'allowed'};
   update({sessionUpdate:'usage_update',used:100,size:1000,_meta:{'_claude/rateLimit':limit}});
  }
  update({sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Working on result.txt'}});
  const timer=setInterval(()=>{
   if(!fs.existsSync(path.join(root,'release')))return;
   clearInterval(timer);
   const definition=JSON.parse(packet.slice(packet.indexOf('{')));
   const git=(...args)=>execFileSync('git',args,{encoding:'utf8'}).trim();
   git('switch','-c',definition.branch);fs.writeFileSync('result.txt','ACP result\n');git('add','result.txt');git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Create result');
   const head=git('rev-parse','HEAD');
   fs.writeFileSync(path.join(root,'pr.json'),JSON.stringify({number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:definition.branch,baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:'COMPLETED',conclusion:'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
   reply(m.id,{stopReason:'end_turn'});
  },5);break;
 }
 default:throw Error('unexpected method '+m.method);
 }
});
`

// Protocol fixtures still use synthetic agents, but exercise Core's managed
// install/start path. The real pinned wrapper and npm integrity are covered by
// TestClaudeACPThroughRunningCore and TestClaudeACPRejectsCorruptTarball.
func syntheticClaudeACPBundle(t *testing.T, root, bundle, program string) {
	t.Helper()
	const pkg = "@agentclientprotocol/claude-agent-acp"
	const version = "0.0.0-fixture"
	dependencies := map[string]string{pkg: version}
	packageJSON, err := json.Marshal(map[string]any{"name": "acp-protocol-fixture", "version": version, "dependencies": dependencies})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha512.Sum512([]byte(program))
	lock, err := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{
		"":                    map[string]any{"version": version, "dependencies": dependencies},
		"node_modules/" + pkg: map[string]any{"version": version, "resolved": "https://registry.npmjs.org/" + pkg + "/-/claude-agent-acp-" + version + ".tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(digest[:])},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	files, _ := manifest["installFiles"].([]any)
	for _, file := range []struct {
		name string
		data []byte
	}{{"package.json", packageJSON}, {"package-lock.json", lock}} {
		name := "adapters/claude-acp/" + file.name
		if err = os.MkdirAll(filepath.Dir(filepath.Join(bundle, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(bundle, name), file.data, 0600); err != nil {
			t.Fatal(err)
		}
		files = append(files, map[string]any{"name": name, "size": len(file.data), "sha256": fmt.Sprintf("%x", sha256.Sum256(file.data))})
	}
	manifest["installFiles"] = files
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bundle, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	script, err := json.Marshal(program)
	if err != nil {
		t.Fatal(err)
	}
	npm := fmt.Sprintf(`#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
const fs=require('node:fs');
if(process.argv[2]!=='ci')throw Error('unexpected fixture npm operation');
const dir='node_modules/@agentclientprotocol/claude-agent-acp';
fs.mkdirSync(dir+'/dist',{recursive:true});
fs.writeFileSync(dir+'/package.json',JSON.stringify({name:'@agentclientprotocol/claude-agent-acp',version:'0.0.0-fixture',type:'module'}));
fs.writeFileSync(dir+'/dist/index.js',%s);
`, script)
	if err = os.WriteFile(filepath.Join(root, "bin/npm"), []byte(npm), 0700); err != nil {
		t.Fatal(err)
	}
}
