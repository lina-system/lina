// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/worker"
	workeracp "github.com/thisisjun786/lina/internal/core/worker/acp"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/lease"
)

func claudeACPBundle(t *testing.T, root, bundle string) {
	t.Helper()
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := fmt.Sprintf(`#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
const fs = require('node:fs');
const {spawnSync} = require('node:child_process');
fs.appendFileSync(%q, JSON.stringify({args:process.argv.slice(2), env:process.env})+'\n');
const result=spawnSync(%q,process.argv.slice(2),{stdio:'inherit'});
process.exit(result.status ?? 1);
`, filepath.Join(root, "npm-calls"), npm)
	if err := os.WriteFile(filepath.Join(root, "bin/npm"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	// Use the production assembly path. It must ship and hash the exact ACP
	// inputs; the scenario never injects release artifacts itself.
	build := exec.CommandContext(t.Context(), "node", "scripts/build.mjs", "--out", bundle)
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("production bundle: %v\n%s", err, output)
	}

}

// The wrapper and SDK are the real pinned npm packages. Only Claude's stream,
// the model and GitHub are synthetic. Core installs and drives the whole chain.
func TestClaudeACPThroughRunningCore(t *testing.T) {
	runACPWorkersThroughRunningCore(t, []acpScenario{{name: "pinned-claude", worker: "claude-acp", binary: "claude", mode: "success", script: claudeStreamStub, bundleFiles: claudeACPBundle,
		beforeDispatch: func(t *testing.T, root string) {
			if _, err := os.Stat(filepath.Join(root, "npm-calls")); !os.IsNotExist(err) {
				t.Fatal("installed before first use", err)
			}
		},
		beforeFinish: func(t *testing.T, h *harness, fixture string, r dispatch.Record) {
			raw, err := os.ReadFile(filepath.Join(fixture, "claude-start"))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Executable string
				Args       []string
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Executable != filepath.Join(filepath.Dir(h.root), "bin/claude") {
				t.Fatal("did not run person's Claude", got)
			}
			args := strings.Join(got.Args, " ")
			if !strings.Contains(args, "--permission-prompt-tool=stdio") || !strings.Contains(args, "--setting-sources=user,project,local") {
				t.Fatal("lost installed tool settings")
			}
			calls, err := os.ReadFile(filepath.Join(filepath.Dir(h.root), "npm-calls"))
			if err != nil {
				t.Fatal(err)
			}
			var call struct {
				Args []string
				Env  map[string]string
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(calls))), &call); err != nil {
				t.Fatal("expected one first-use install", err)
			}
			flags := strings.Join(call.Args, " ")
			for _, flag := range []string{"ci ", "--ignore-scripts", "--omit=dev", "--omit=optional", "--registry=https://registry.npmjs.org/"} {
				if !strings.Contains(flags, flag) {
					t.Fatal("missing install flag", flag)
				}
			}
			if call.Env["LINA_RESPONSES_API_KEY"] != "" || call.Env["NODE_OPTIONS"] != "" || call.Env["NPM_TOKEN"] != "" {
				t.Fatal("install inherited credentials or Node injection")
			}
			source := filepath.Join(h.root, "system/adapters/claude-acp")
			dirs, err := filepath.Glob(filepath.Join(source, "runtime", "*", "node_modules"))
			if err != nil || len(dirs) != 1 {
				t.Fatal("Core did not install pinned wrapper", dirs, err)
			}
			var lock struct {
				Packages map[string]struct{ Optional bool }
			}
			raw, err = os.ReadFile(filepath.Join(source, "package-lock.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &lock); err != nil {
				t.Fatal(err)
			}
			for name, p := range lock.Packages {
				if p.Optional {
					if _, err := os.Stat(filepath.Join(filepath.Dir(dirs[0]), name)); !os.IsNotExist(err) {
						t.Fatal("optional package installed", name, err)
					}
				}
			}
			if r.Version != "fixture-unverified-9" {
				t.Fatal("wrapper version replaced Claude version", r.Version)
			}
		},
		afterFinish: func(t *testing.T, h *harness, fixture string, r dispatch.Record) {
			var result workeracp.Result
			if err := json.Unmarshal(r.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.AgentVersion != "0.94.0" || result.StopReason != "end_turn" {
				t.Fatal("program version or completion missing", result)
			}
			l := lease.New(func() {})
			defer l.Release()
			program, err := install.ClaudeACP(t.Context(), l, filepath.Join(h.root, "system/adapters/claude-acp"))
			if err != nil {
				t.Fatal("reuse", err)
			}
			if _, err := os.Stat(program); err != nil {
				t.Fatal(err)
			}
		},
	}})
}

const claudeStreamStub = `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
function messageText(text) {
 if (!text.startsWith('<lina-message ')) return text;
 const match=text.match(/^<lina-message [^>]*><text>([\s\S]*)<\/text><\/lina-message>$/);
 if (!match) throw Error('invalid worker message frame');
 return match[1].replace(/&(#x[0-9a-f]+|#[0-9]+|amp|lt|gt|quot|apos);/gi,(_,e)=>e[0]==='#'?String.fromCodePoint(e[1].toLowerCase()==='x'?parseInt(e.slice(2),16):Number(e.slice(1))):({amp:'&',lt:'<',gt:'>',quot:'"',apos:"'"})[e]);
}
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import {execFileSync} from 'node:child_process';
const root=process.env.LINA_ACP_FIXTURE;
if(path.basename(process.argv[1])==='gh'){
 if(process.argv[2]==='api')console.log('[[]]');
 else console.log(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
 process.exit(0);
}
if(process.env.LINA_RESPONSES_API_KEY)throw Error('Core credential leaked');
if(process.argv[2]==='--version'){console.log('fixture-unverified-9');process.exit(0)}
if(process.argv[2]==='auth'){console.log(JSON.stringify({loggedIn:true,authMethod:'claude.ai',subscriptionType:'max'}));process.exit(0)}
if(process.env.CLAUDE_CODE_EXECUTABLE!==process.argv[1])throw Error('wrong Claude executable');
fs.writeFileSync(path.join(root,'credential-filtered'),'1');
fs.writeFileSync(path.join(root,'claude-start'),JSON.stringify({Executable:process.argv[1],Args:process.argv.slice(2)}));
const send=x=>console.log(JSON.stringify(x));
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line);
 if(m.type==='control_request'){
  let response={};
  if(m.request.subtype==='initialize')response={commands:[],models:[{value:'fixture',displayName:'Fixture',description:'Synthetic model'}],agents:[],account:{tokenSource:'fixture'},model:'fixture'};
  if(m.request.subtype==='mcp_status')response={mcpServers:[]};
  send({type:'control_response',response:{subtype:'success',request_id:m.request_id,response}});return;
 }
 if(m.type!=='user')return;
 const content=m.message.content;
 const packet=typeof content==='string'?content:content.filter(c=>c.type==='text').map(c=>c.text).join('\n');
 send({type:'assistant',uuid:'message-1',session_id:'fixture-session',parent_tool_use_id:null,message:{id:'message-1',type:'message',role:'assistant',model:'fixture',content:[{type:'text',text:'Working on result.txt'}],stop_reason:null,usage:{input_tokens:1,output_tokens:1}}});
 const timer=setInterval(()=>{
  if(!fs.existsSync(path.join(root,'release')))return;
  clearInterval(timer);
  const definition=JSON.parse(messageText(packet).slice(messageText(packet).indexOf('{')));
  const git=(...args)=>execFileSync('git',args,{encoding:'utf8'}).trim();
  git('switch','-c',definition.branch);fs.writeFileSync('result.txt','ACP result\n');git('add','result.txt');git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Create ACP result');
  const head=git('rev-parse','HEAD');
  fs.writeFileSync(path.join(root,'pr.json'),JSON.stringify({number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:definition.branch,baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:'COMPLETED',conclusion:'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
  send({type:'result',subtype:'success',uuid:'result-1',session_id:'fixture-session',is_error:false,result:'Created result.txt',duration_ms:1,duration_api_ms:1,num_turns:1,total_cost_usd:0,usage:{input_tokens:1,output_tokens:1},modelUsage:{},permission_denials:[]});
 },5);
});
`

// Opt in separately; CI never invokes a person's Claude account.
func TestClaudeACPLive(t *testing.T) {
	if os.Getenv("LINA_LIVE_CLAUDE_ACP") != "1" {
		t.Skip("set LINA_LIVE_CLAUDE_ACP=1 to use installed Claude")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	source := t.TempDir()
	for _, name := range []string{"package.json", "package-lock.json"} {
		data, err := os.ReadFile(filepath.Join("../../adapters/claude-acp", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var profile workeracp.Profile
	for _, p := range workeracp.Installed() {
		if p.Name == "claude-acp" {
			profile = p
		}
	}
	if profile.Name == "" {
		t.Fatal("installed claude and node required")
	}
	l := lease.New(func() {})
	defer l.Release()
	a := workeracp.New(workeracp.Config{Profile: profile, Epoch: 1, Leaser: claudeLiveLeaser{}, Prepare: func(ctx context.Context, l lease.Lease) (string, error) { return install.ClaudeACP(ctx, l, source) }})
	defer a.Close(context.Background())
	version, err := a.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed Claude version: %s", version)
	_, err = a.Start(ctx, l, worker.StartSpec{TaskID: "live", AttemptID: "1", Workdir: t.TempDir(), TaskPacket: "Reply with OK. Do not use tools or change files."})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case e := <-a.Events():
			if e.Finish != nil {
				if e.Finish.Status != worker.Done {
					t.Fatalf("live finish: %+v", e.Finish)
				}
				t.Logf("ACP program version: %s", a.Result().AgentVersion)
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

type claudeLiveLeaser struct{}

func (claudeLiveLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

func TestClaudeACPMissingToolLeavesOtherWorkersRunning(t *testing.T) {
	runACPWorkersThroughRunningCore(t, []acpScenario{{name: "missing-claude", worker: "gemini", binary: "gemini", unavailable: "claude-acp", mode: "success", bundleFiles: claudeACPBundle,
		afterFinish: func(t *testing.T, h *harness, _ string, _ dispatch.Record) {
			if _, err := os.Stat(filepath.Join(filepath.Dir(h.root), "npm-calls")); !os.IsNotExist(err) {
				t.Fatal("installed wrapper for missing Claude", err)
			}
		},
	}})
}

func TestClaudeACPRejectsCorruptTarball(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"package.json", "package-lock.json"} {
		data, err := os.ReadFile(filepath.Join("../../adapters/claude-acp", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "package-lock.json" {
			var lock map[string]any
			if err := json.Unmarshal(data, &lock); err != nil {
				t.Fatal(err)
			}
			entry := lock["packages"].(map[string]any)["node_modules/@agentclientprotocol/claude-agent-acp"].(map[string]any)
			entry["integrity"] = "sha512-" + strings.Repeat("A", 86) + "=="
			data, _ = json.Marshal(lock)
		}
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	l := lease.New(func() {})
	defer l.Release()
	if _, err := install.ClaudeACP(t.Context(), l, source); err == nil {
		t.Fatal("npm accepted wrong SHA-512 integrity")
	}
	entries, err := os.ReadDir(filepath.Join(source, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("failed install left runnable state", entries)
	}
}
