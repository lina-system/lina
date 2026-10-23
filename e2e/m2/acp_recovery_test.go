// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Core, its process supervision, dispatch ledger, worktree and commits are real.
// The agent is killed only after its progress is durable, while its prompt is open.
func TestACPRecoveryThroughRunningCore(t *testing.T) {
	var scenarios []acpScenario
	for _, mode := range []string{"load", "resume", "fresh", "second-death", "unknown-effect", "completed-tool"} {
		scenarios = append(scenarios, acpScenario{
			name: mode, worker: "gemini", binary: "gemini", mode: mode,
			script: acpRecoveryScript,
			beforeFinish: func(t *testing.T, h *harness, fixture string, initial dispatch.Record) {
				if mode == "completed-tool" {
					coordinateBeforeACPDeath(t, h, fixture)
				}
				killACPAttempt(t, fixture, 1)
				if mode == "unknown-effect" || mode == "completed-tool" {
					return
				}
				waitACPRecovery(t, h, func(r dispatch.Record, card work.Card) bool {
					if r.State == "unknown" || r.State == "finished" || r.State == "decision" {
						t.Fatalf("worker died without session recovery: state=%s code=%s attempts=%d recoveries=%d", r.State, r.Code, r.Attempt, r.Recoveries)
					}
					if r.Attempt != 2 || card.Run == nil || card.Run.Generation != 2 || !card.Active || len(card.Stages) == 0 || !strings.Contains(card.Stages[0].Summary, "Continued result.txt") {
						return false
					}
					if r.Recoveries != 1 || r.Thread == nil || initial.Thread == nil || r.Thread.ProcessGeneration == initial.Thread.ProcessGeneration || r.Branch != initial.Branch {
						t.Fatalf("recovery lost task binding: %+v", r)
					}
					if mode != "fresh" && r.Thread.ID != initial.Thread.ID {
						t.Fatal("recovery changed the recorded session")
					}
					return true
				})
				if mode == "second-death" {
					killACPAttempt(t, fixture, 2)
				}
			},
			afterFinish: func(t *testing.T, h *harness, fixture string, record dispatch.Record) {
				wantStarts := 2
				if mode == "unknown-effect" || mode == "completed-tool" {
					wantStarts = 1
					if record.State != "unknown" || record.Recoveries != 0 {
						t.Fatal("unknown tool effect authorized recovery", record)
					}
				} else if mode == "second-death" {
					if record.State != "decision" || record.Recoveries != 1 || record.Attempt != 2 {
						t.Fatal("second death did not stop at a decision", record)
					}
					waitACPDecision(t, h, record.ID)
				} else {
					if record.State != "finished" || record.Code != "ready_for_review" || record.PR == nil || record.Recoveries != 1 {
						t.Fatal("recovered work did not produce verified result", record)
					}
					data, err := os.ReadFile(filepath.Join(h.root, "work/tasks", record.ID, "result.txt"))
					if err != nil || string(data) != "ACP recovered result\n" {
						t.Fatalf("result %q: %v", data, err)
					}
				}
				var starts []struct {
					PID int
					Cwd string
				}
				readACPFixture(t, fixture, "starts.json", &starts)
				if len(starts) != wantStarts {
					t.Fatalf("got %d starts, want %d", len(starts), wantStarts)
				}
				for _, start := range starts {
					if start.Cwd != filepath.Join(h.root, "work/tasks", record.ID) {
						t.Fatal("worker moved worktree", start.Cwd)
					}
				}
				if mode != "unknown-effect" && mode != "completed-tool" {
					var restored struct{ Method, SessionID string }
					readACPFixture(t, fixture, "restored.json", &restored)
					want := "session/load"
					if mode == "resume" {
						want = "session/resume"
					}
					if mode == "fresh" {
						want = "session/new"
					}
					if restored.Method != want || mode != "fresh" && restored.SessionID != "acp-recovery" {
						t.Fatalf("wrong recovery request: %+v", restored)
					}
				}
				db := acpRecoveryDB(t, h)
				ledger, err := work.New(db).Ledger(h.ctx, record.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range ledger {
					if strings.Contains(event.Value, "REPLAY_ONLY") {
						t.Fatal("loaded history was published as new progress")
					}
				}
				if strings.Contains(string(record.Result), "REPLAY_ONLY") {
					t.Fatal("loaded history leaked into the new result")
				}
			},
		})
	}
	runACPWorkersThroughRunningCore(t, scenarios)
}

func coordinateBeforeACPDeath(t *testing.T, h *harness, fixture string) {
	t.Helper()
	var server struct {
		Env []struct{ Name, Value string }
	}
	readACPFixture(t, fixture, "old-server.json", &server)
	var socket, token string
	for _, env := range server.Env {
		switch env.Name {
		case "LINA_WORKER_TOOLS_SOCKET":
			socket = env.Value
		case "LINA_WORKER_TOOLS_TOKEN":
			token = env.Value
		}
	}
	response, err := clientapi.CallWorkerTool(h.ctx, socket, clientapi.WorkerToolRequest{Token: token, Method: "tools/call", Name: "coordinate", Arguments: json.RawMessage(`{"request":"Retain this coordination request."}`)})
	var mcp struct {
		IsError bool
		Content []struct{ Text string }
	}
	if err != nil || response.Error != "" || json.Unmarshal(response.Result, &mcp) != nil || mcp.IsError || len(mcp.Content) != 1 {
		t.Fatal("coordination failed", err)
	}
	var ack struct {
		Accepted bool
		EventID  string `json:"event_id"`
	}
	if json.Unmarshal([]byte(mcp.Content[0].Text), &ack) != nil || !ack.Accepted || ack.EventID == "" {
		t.Fatal("coordination not acknowledged")
	}
	// The real Core acknowledged the call, committed its request and cleared
	// pending state. No optional ACP tool-call update was sent by the agent.
	waitACPRecovery(t, h, func(r dispatch.Record, _ work.Card) bool { return len(r.PendingRequests) == 0 })
	db := acpRecoveryDB(t, h)
	var count int
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE id=?", ack.EventID).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("coordination not durable: count=%d error=%v", count, err)
	}
}

func killACPAttempt(t *testing.T, fixture string, attempt int) {
	t.Helper()
	var starts []struct {
		PID int
		Cwd string
	}
	readACPFixture(t, fixture, "starts.json", &starts)
	if len(starts) != attempt {
		t.Fatalf("starts=%d before death %d", len(starts), attempt)
	}
	p, err := os.FindProcess(starts[attempt-1].PID)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	if err = p.Kill(); err != nil {
		t.Fatal(err)
	}
}

func acpRecoveryDB(t *testing.T, h *harness) *canon.Store {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

func waitACPRecovery(t *testing.T, h *harness, ready func(dispatch.Record, work.Card) bool) {
	t.Helper()
	db := acpRecoveryDB(t, h)
	for {
		var raw []byte
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch LIMIT 1").Scan(&raw)
		}); err != nil {
			t.Fatal(err)
		}
		var record dispatch.Record
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		card, err := work.New(db).Get(h.ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ready(record, card) {
			return
		}
		select {
		case <-h.ctx.Done():
			t.Fatalf("recovery did not advance: %+v", record)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitACPDecision(t *testing.T, h *harness, id string) {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "recovery-reader", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for {
		if _, err = c.Waiting(h.ctx, protocol.WaitingGet{}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		list, ok := frame.Payload.(protocol.Waiting)
		if ok && list.Rows != nil {
			for _, row := range *list.Rows {
				if row.Ref.Source == "worker" && row.Ref.Key == "worker-request:recovery:dispatch:"+id {
					db := acpRecoveryDB(t, h)
					var count int
					if err := db.Read(h.ctx, func(tx *canon.Tx) error {
						return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM worker_recovery WHERE id=? AND json_extract(record,'$.State')='decision' AND json_extract(record,'$.Retries')=1", "dispatch:"+id).Scan(&count)
					}); err != nil || count != 1 {
						t.Fatalf("decision not durable: count=%d error=%v", count, err)
					}
					return
				}
			}
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("second death has no waiting decision row")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

const acpRecoveryScript = `#!/usr/bin/env node
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
const root=process.env.LINA_ACP_FIXTURE, mode=process.env.LINA_ACP_CASE;
const file=name=>path.join(root,name);
if(path.basename(process.argv[1])==='gh') {
 console.log(process.argv[2]==='api'?'[[]]':fs.readFileSync(file('pr.json'),'utf8'));process.exit(0);
}
if(process.argv[2]==='--version'){console.log('fixture-unverified-9');process.exit(0);}
if(process.env.LINA_RESPONSES_API_KEY)throw Error('Core credential leaked');
fs.writeFileSync(file('credential-filtered'),'1');
const starts=fs.existsSync(file('starts.json'))?JSON.parse(fs.readFileSync(file('starts.json'))):[];
starts.push({PID:process.pid,Cwd:process.cwd()});fs.writeFileSync(file('starts.json'),JSON.stringify(starts));
if(starts.length>2)throw Error('third automatic start');
const attempt=starts.length;
let session='acp-recovery', pending, packet;
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x}));
const reply=(id,result)=>send({id,result});
const update=update=>send({method:'session/update',params:{sessionId:session,update}});
const progress=text=>update({sessionUpdate:'agent_message_chunk',content:{type:'text',text}});
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line);
 switch(m.method){
 case 'initialize':
  reply(m.id,{protocolVersion:1,agentInfo:{name:'fixture',version:'fixture-unverified-9'},agentCapabilities:{loadSession:mode!=='fresh',...(mode==='resume'?{sessionCapabilities:{resume:{}}}:{})}});break;
 case 'session/new':case 'session/load':case 'session/resume':
  if(m.params.cwd!==process.cwd()||m.params.mcpServers.length!==1||m.params.mcpServers[0].name!=='lina')throw Error('wrong worktree/tools');
  if(attempt===1){
   if(m.method!=='session/new')throw Error('initial session not new');
   fs.writeFileSync('retained.txt','uncommitted work');
   fs.writeFileSync(file('old-server.json'),JSON.stringify(m.params.mcpServers[0]));
  }else{
   if(m.method!==(mode==='resume'?'session/resume':mode==='fresh'?'session/new':'session/load'))throw Error('wrong recovery method');
   if(m.method!=='session/new'&&m.params.sessionId!==session)throw Error('session id lost');
   if(fs.readFileSync('retained.txt','utf8')!=='uncommitted work')throw Error('worktree reset');
   if(JSON.stringify(m.params.mcpServers[0])===fs.readFileSync(file('old-server.json'),'utf8'))throw Error('old tool binding reused');
   fs.writeFileSync(file('restored.json'),JSON.stringify({Method:m.method,SessionID:m.params.sessionId||'fresh-session'}));
   if(mode==='fresh')session='fresh-session';
   if(m.method==='session/load')for(let i=0;i<1024;i++)progress('REPLAY_ONLY '+i);
  }
  reply(m.id,m.method==='session/new'?{sessionId:session}:{});break;
 case 'session/prompt':
  if(m.params.sessionId!==session)throw Error('wrong prompt session');
  pending=m.id;packet=m.params.prompt[0].text;
  if(mode==='unknown-effect')update({sessionUpdate:'tool_call',toolCallId:'unknown',title:'Unsettled external operation',status:'in_progress'});
  progress(attempt===1?'Working on result.txt':'Continued result.txt');
  if(attempt===1)break;
  const timer=setInterval(()=>{
   if(!fs.existsSync(file('release')))return;
   clearInterval(timer);
   const definition=JSON.parse(messageText(packet).slice(messageText(packet).indexOf('{')));
   const git=(...args)=>execFileSync('git',args,{encoding:'utf8'}).trim();
   git('switch','-c',definition.branch);
   fs.unlinkSync('retained.txt');fs.writeFileSync('result.txt','ACP recovered result\n');git('add','result.txt');git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Finish recovered work');
   const head=git('rev-parse','HEAD');
   fs.writeFileSync(file('pr.json'),JSON.stringify({number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:definition.branch,baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:'COMPLETED',conclusion:'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
   reply(pending,{stopReason:'end_turn'});
  },5);break;
 default:throw Error('unexpected method '+m.method);
 }
});
`
