// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

// Two installed Cores execute real work concurrently. Each synthetic ACP agent
// tries the other live session's token against its own server before submitting
// a claim. No vendor, network credentials or existing worker settings are used.
func TestACPWorkToolsThroughRunningCore(t *testing.T) {
	peers := t.TempDir()
	var scenarios []acpScenario
	for _, id := range []string{"a", "b"} {
		scenarios = append(scenarios, acpScenario{name: id, worker: "gemini", binary: "gemini", mode: "success", script: acpWorkToolsScript, parallel: true, answerQuestions: true,
			environment: []string{"LINA_ACP_PEERS=" + peers, "LINA_ACP_PEER_ID=" + id},
			setup: func(t *testing.T, root string) {
				for file, body := range workerSettings {
					path := filepath.Join(root, file)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}, beforeFinish: verifyACPWorkTools, afterFinish: verifyACPWorkToolsEnded})
	}
	runACPWorkersThroughRunningCore(t, scenarios)
}

var workerSettings = map[string]string{
	".gemini/settings.json": `{"mcpServers":{"personal":{"command":"own-tool"}}}`,
	".cursor/mcp.json":      `{"mcpServers":{"personal":{"command":"own-tool"}}}`,
	".claude.json":          `{"mcpServers":{"personal":{"command":"own-tool"}}}`,
}

func readACPFixture(t *testing.T, dir, file string, out any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func verifyACPWorkTools(t *testing.T, h *harness, fixture string, record dispatch.Record) {
	t.Helper()
	var listed struct {
		Tools []struct {
			Name        string
			InputSchema any
		}
	}
	readACPFixture(t, fixture, "listed.json", &listed)
	want := worker.WorkToolDeclarations()
	if len(listed.Tools) != 4 {
		t.Fatal("agent did not list four work tools")
	}
	for i, tool := range listed.Tools {
		var schema any
		json.Unmarshal(want[i].InputSchema, &schema)
		if tool.Name != want[i].Name || !reflect.DeepEqual(tool.InputSchema, schema) {
			t.Fatal("ACP and Codex tool contracts differ")
		}
	}
	var answer struct{ Answer, Action string }
	readACPFixture(t, fixture, "answered.json", &answer)
	if answer.Answer != "Continue." || answer.Action != "accept" {
		t.Fatal("question did not reach its worker")
	}
	var receipt work.Receipt
	var ack struct {
		Accepted, Verified bool
		EventID            string `json:"event_id"`
	}
	readACPFixture(t, fixture, "claim.json", &receipt)
	readACPFixture(t, fixture, "accepted.json", &ack)
	if record.Thread == nil {
		t.Fatal("no dispatched thread")
	}
	th := record.Thread
	if !ack.Accepted || ack.Verified || receipt.TurnRef.ThreadID != th.ID || receipt.TurnRef.TurnID != th.TurnID || receipt.RelationshipID != record.ID || receipt.ExecutionGeneration != record.Attempt {
		t.Fatal("unbound or verified worker claim")
	}
	expected := fmt.Sprintf("worker-request:%d:%d:%s:receipt:%s", th.ProcessGeneration, th.BindingEpoch, th.ID, receipt.EventID)
	if ack.EventID != expected {
		t.Fatal("claim lost process generation or binding epoch")
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var body string
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT body FROM conv_event WHERE id=?", expected).Scan(&body)
	}); err != nil {
		t.Fatal("claim not durable", err)
	}
	var stored work.Receipt
	start := strings.Index(body, "{")
	if start < 0 || json.Unmarshal([]byte(body[start:]), &stored) != nil || !reflect.DeepEqual(receipt, stored) {
		t.Fatal("durable claim changed")
	}
	card, err := work.New(db).Get(h.ctx, record.ID)
	if err != nil || !card.Active || card.Receipt != nil {
		t.Fatal("worker claim completed the task")
	}
	var refused struct{ Error json.RawMessage }
	readACPFixture(t, fixture, "foreign.json", &refused)
	if len(refused.Error) == 0 {
		t.Fatal("foreign live session token accepted")
	}
}

func verifyACPWorkToolsEnded(t *testing.T, h *harness, fixture string, _ dispatch.Record) {
	t.Helper()
	var server struct {
		Command string
		Args    []string
		Env     []struct{ Name, Value string }
	}
	readACPFixture(t, fixture, "server.json", &server)
	c := exec.CommandContext(h.ctx, server.Command, server.Args...)
	c.Env = []string{}
	for _, env := range server.Env {
		c.Env = append(c.Env, env.Name+"="+env.Value)
	}
	c.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\"}}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"submit_receipt\",\"arguments\":{}}}\n")
	output, err := c.Output()
	if err != nil {
		t.Fatal("late MCP probe failed", err)
	}
	lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
	var reply struct {
		ID    int
		Error json.RawMessage
	}
	if len(lines) != 2 || json.Unmarshal(lines[1], &reply) != nil || reply.ID != 2 || len(reply.Error) == 0 {
		t.Fatal("ended turn accepted a call")
	}
	for file, body := range workerSettings {
		got, err := os.ReadFile(filepath.Join(filepath.Dir(h.root), file))
		if err != nil || string(got) != body {
			t.Fatal("worker settings changed", file)
		}
	}
	for _, env := range server.Env {
		if env.Name == "LINA_WORKER_TOOLS_SOCKET" {
			if _, err := os.Stat(env.Value); !os.IsNotExist(err) {
				t.Fatal("session socket remains")
			}
		}
	}
}

const acpWorkToolsScript = `#!/usr/bin/env node
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
import {spawn,execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
const root=process.env.LINA_ACP_FIXTURE;
const save=(name,value)=>fs.writeFileSync(path.join(root,name),JSON.stringify(value));
if(path.basename(process.argv[1])==='gh') {
 if(process.argv[2]==='api') console.log('[[]]');
 else console.log(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
 process.exit(0);
}
if(process.env.LINA_RESPONSES_API_KEY) throw Error('Core credential leaked');
if(process.argv[2]==='--version') { console.log('fixture-unverified-9'); process.exit(0); }
fs.writeFileSync(path.join(root,'credential-filtered'),'1');
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x}));
const reply=(id,result)=>send({id,result});
const sid='session-'+process.env.LINA_ACP_PEER_ID;
const progress=()=>send({method:'session/update',params:{sessionId:sid,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Working on result.txt'}}}});
const waitFile=file=>new Promise(resolve=>{ const timer=setInterval(()=>{ if(fs.existsSync(file)){clearInterval(timer);resolve()} },5) });
function mcp(server,token) {
 const env=Object.fromEntries(server.env.map(e=>[e.name,e.value]));
 if(token)env.LINA_WORKER_TOOLS_TOKEN=token;
 const child=spawn(server.command,server.args,{env,stdio:['pipe','pipe','inherit']});
 let seq=0; const pending=new Map();
 readline.createInterface({input:child.stdout}).on('line',line=>{const r=JSON.parse(line);const p=pending.get(r.id);if(p){pending.delete(r.id);p.resolve(r)}});
 child.on('error',e=>{for(const p of pending.values())p.reject(e)});
 child.on('exit',()=>{for(const p of pending.values())p.reject(Error('MCP exited'))});
 return {call:(method,params={})=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});child.stdin.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n')}),close:()=>new Promise(resolve=>{child.on('exit',resolve);child.stdin.end()})};
}
const unwrap=response=>{if(response.error||response.result.isError)throw Error('work tool refused');return JSON.parse(messageText(response.result.content[0].text))};
let server,bridge;
readline.createInterface({input:process.stdin}).on('line',async line=>{
 try {
 const m=JSON.parse(line);
 switch(m.method) {
 case 'initialize':reply(m.id,{protocolVersion:1,agentInfo:{name:'fixture',version:'fixture-unverified-9'}});break;
 case 'session/new':
  if(m.params.cwd!==process.cwd()||m.params.mcpServers.length!==1)throw Error('invalid session setup');
  server=m.params.mcpServers[0];
  if(server.name!=='lina'||JSON.stringify(server.args)!==JSON.stringify(['worker-tools']))throw Error('invalid MCP server');
  save('server.json',server);
  bridge=mcp(server);
  await bridge.call('initialize',{protocolVersion:'2025-06-18',capabilities:{},clientInfo:{name:'fixture',version:'1'}});
  const list=await bridge.call('tools/list');if(list.error)throw Error('tools/list failed');save('listed.json',list.result);
  fs.writeFileSync(path.join(process.env.LINA_ACP_PEERS,process.env.LINA_ACP_PEER_ID),server.env.find(e=>e.name==='LINA_WORKER_TOOLS_TOKEN').value,{mode:0o600});
  reply(m.id,{sessionId:sid});break;
 case 'session/prompt': {
  const peer=path.join(process.env.LINA_ACP_PEERS,process.env.LINA_ACP_PEER_ID==='a'?'b':'a');
  await waitFile(peer);
  const foreign=mcp(server,fs.readFileSync(peer,'utf8'));
  await foreign.call('initialize',{protocolVersion:'2025-06-18'});
  const denied=await foreign.call('tools/call',{name:'read_projection',arguments:{}});
  if(!denied.error)throw Error('foreign token accepted');save('foreign.json',denied);await foreign.close();
  const projection=unwrap(await bridge.call('tools/call',{name:'read_projection',arguments:{}}));
  const answer=unwrap(await bridge.call('tools/call',{name:'ask',arguments:{question:'Continue with the result?'}}));
  if(answer.answer!=='Continue.'||answer.action!=='accept')throw Error('question answer lost');save('answered.json',answer);
  const coord=unwrap(await bridge.call('tools/call',{name:'coordinate',arguments:{request:'Preparing the result.'}}));
  if(!coord.accepted)throw Error('coordination not recorded');
  const packet=m.params.prompt[0].text,definition=JSON.parse(messageText(packet).slice(messageText(packet).indexOf('{')));
  const git=(...args)=>execFileSync('git',args,{encoding:'utf8'}).trim();
  git('switch','-c',definition.branch);fs.writeFileSync('result.txt','ACP result\n');git('add','result.txt');git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Create ACP result');
  const head=git('rev-parse','HEAD');
  save('pr.json',{number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:definition.branch,baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:'COMPLETED',conclusion:'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]});
  const hash=s=>createHash('sha256').update(s).digest('hex'),artifact=path.join(process.cwd(),'result.txt'),sha=hash(fs.readFileSync(artifact)),now=new Date().toISOString();
  const receipt={schema_version:1,relationshipId:projection.assignment_id,executionGeneration:projection.execution_generation,attempt:1,outcome:'ready_for_review',producer:'child',turnRef:{threadId:projection.thread_id,turnId:projection.turn_id,turnStatus:'completed'},emittedAt:now,manifest:[{path:artifact,sha256:sha}],provenance:[{artifact_id:'worker-result',task_id:projection.assignment_id,node_id:projection.node_id,path:artifact,sha256:sha,observed_at:now}],revisionHash:hash(artifact+':'+sha)};
  receipt.eventId=hash([receipt.relationshipId,receipt.executionGeneration,receipt.revisionHash,receipt.outcome].join('|')).slice(0,32);
  save('claim.json',receipt);
  save('accepted.json',unwrap(await bridge.call('tools/call',{name:'submit_receipt',arguments:{receipt}})));
  progress();await waitFile(path.join(root,'release'));await bridge.close();reply(m.id,{stopReason:'end_turn'});break;
 }
 default:throw Error('unexpected method '+m.method);
 }
 } catch(e) { console.error(e);process.exit(1) }
});
`
