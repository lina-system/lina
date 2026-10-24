// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

// Only model, worker and GitHub protocols are synthetic. Core, admission, the
// tool bridge, work ledger, recovery, supervised processes and Git are real.
func TestWorkerTextCallsThroughRunningCore(t *testing.T) {
	for _, adapter := range []string{"acp", "codex"} {
		for _, mode := range []string{"coordinate", "_check_inbox", "_submit_receipt", "restart", "incomplete", "second-failure"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) { textCallCore(t, adapter, mode) })
		}
	}
}
func textCallCore(t *testing.T, adapter, mode string) {
	root := t.TempDir()
	bin, repo := filepath.Join(root, "bin"), filepath.Join(root, "repo")
	for _, dir := range []string{bin, repo} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"git", "node", "sh"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	binary := "gemini"
	if adapter == "codex" {
		binary = "codex"
	}
	for _, name := range []string{binary, "gh"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(textCallWorkerScript), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("LINA_TEXT_FIXTURE", root)
	t.Setenv("LINA_TEXT_MODE", mode)
	t.Setenv("LINA_TEXT_ADAPTER", adapter)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	git := func(args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), filepath.Join(bin, "git"), args...)
		c.Dir = repo
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	if err := os.WriteFile(filepath.Join(repo, "design.md"), []byte("# Result\nCreate result.txt.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "design.md")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
	git("remote", "add", "origin", "https://github.com/fixture/project.git")
	var dispatched atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := decodeRequest(t, r)
		item := last(q)
		declared := func(name string) bool {
			for _, raw := range q.Tools {
				var v struct{ Name string }
				_ = json.Unmarshal(raw, &v)
				if v.Name == name {
					return true
				}
			}
			return false
		}
		if declared("coding_decision") {
			if item.Type == "function_call_output" {
				reply(w, "Decision recorded.")
				return
			}
			decision := "delegate"
			raw, _ := json.Marshal(q.Input)
			if strings.Contains(string(raw), "coding_delivery") {
				decision = "unverified"
			}
			toolCall(w, "manager", "coding_decision", map[string]string{"decision": decision, "evidence": "Synthetic worker evidence."})
			return
		}
		if item.Type == "function_call_output" {
			result(t, item, nil)
		}
		if !dispatched.Load() {
			if !declared("dispatch_work") {
				toolCall(w, "load", "load_tools", map[string]any{"tools": []string{"dispatch_work"}})
				return
			}
			dispatched.Store(true)
			toolCall(w, "dispatch", "dispatch_work", map[string]any{"repository": repo, "base": "dev", "branch": "text-result", "what": "Create result.txt", "why": "Verify text-call recovery", "scope": []string{"result.txt"}, "not_touch": []string{"design.md"}, "done": "Committed result", "design_refs": []string{"design.md#result"}, "write_area": []string{"result.txt"}})
			return
		}
		reply(w, "Task accepted.")
	}))
	defer model.Close()
	h := newCore(t, model.URL)
	h.turn(h.agent("dev"), "text-call", "Create the result in the fixture repository.")
	state := "finished"
	starts := 1
	if mode == "restart" || mode == "incomplete" || mode == "second-failure" {
		starts = 2
	}
	if mode == "second-failure" {
		state = "decision"
	}
	h.await("SELECT count(*) FROM worker_dispatch WHERE state=?", 1, state)
	var record dispatch.Record
	h.read(func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch").Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &record)
	})
	if record.Attempt != starts || record.Recoveries != starts-1 {
		t.Fatalf("attempts=%d recoveries=%d code=%s", record.Attempt, record.Recoveries, record.Code)
	}
	if starts == 1 {
		if record.Snapshot == nil || record.Snapshot.Finish == nil || record.Snapshot.Finish.Code != "text_tool_call_recovered" {
			t.Fatalf("work tool did not complete through Core: %+v", record)
		}
		if mode == "coordinate" {
			h.await("SELECT count(*) FROM conv_event WHERE CAST(body AS TEXT) LIKE '%Text call coordinated%'", 1)
		}
		if mode == "_submit_receipt" {
			h.await("SELECT count(*) FROM conv_event WHERE CAST(body AS TEXT) LIKE '%Worker completion claim (unverified)%'", 1)
		}
	}
	if state == "decision" {
		if record.Code != "text_tool_call" {
			t.Fatal(record.Code)
		}
		h.await("SELECT count(*) FROM worker_recovery WHERE id=? AND json_extract(record,'$.State')='decision'", 1, "dispatch:"+record.ID)
		awaitTextCallDecision(t, h, record.ID)
	} else {
		if record.Code != "ready_for_review" || record.PR == nil {
			t.Fatalf("result not verified: %+v", record)
		}
		raw, err := os.ReadFile(filepath.Join(h.root, "work/tasks", record.ID, "result.txt"))
		if err != nil || string(raw) != "Recovered result\n" {
			t.Fatalf("artifact=%q %v", raw, err)
		}
	}
	// A terminal row is durable: stop the scheduler before checking exact starts.
	h.stop()
	raw, err := os.ReadFile(filepath.Join(root, "starts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sessions []struct{ Method, Session, Cwd, Branch, Packet string }
	if err = json.Unmarshal(raw, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != starts {
		t.Fatalf("sessions=%s", raw)
	}
	for i, s := range sessions {
		want := "session/new"
		if adapter == "codex" {
			want = "thread/start"
		}
		if s.Method != want || s.Cwd != filepath.Join(h.root, "work/tasks", record.ID) {
			t.Fatalf("wrong session: %+v", s)
		}
		if i > 0 && (s.Session == sessions[0].Session || s.Branch != sessions[0].Branch || s.Packet == sessions[0].Packet || !strings.Contains(s.Packet, record.ID) || !strings.Contains(s.Packet, record.Branch) || strings.Contains(s.Packet, "[tool_use")) {
			t.Fatalf("bad fresh continuation: %+v", s)
		}
	}
	h.read(func(tx *canon.Tx) error {
		// Scan both durable progress and receipts, including raw text JSON escaping.
		for _, table := range []string{"work_ledger", "worker_dispatch", "conv_event"} {
			column := "record"
			if table == "conv_event" {
				column = "body"
			}
			var leaked int
			if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM "+table+" WHERE CAST("+column+" AS TEXT) LIKE '%[tool_use%' OR CAST("+column+" AS TEXT) LIKE '%tool_use id=%'").Scan(&leaked); err != nil {
				return err
			}
			if leaked != 0 {
				t.Errorf("text-call markup leaked into %s", table)
			}
		}
		return nil
	})
}

func awaitTextCallDecision(t *testing.T, h *coreHarness, id string) {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "text-call-reader", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = c.Waiting(h.ctx, protocol.WaitingGet{}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if list, ok := frame.Payload.(protocol.Waiting); ok && list.Rows != nil {
			for _, row := range *list.Rows {
				if row.Ref.Source == "worker" && row.Ref.Key == "worker-request:recovery:dispatch:"+id {
					return
				}
			}
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("missing recovery decision row")
		case <-ticker.C:
		}
	}
}

const textCallWorkerScript = `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const root=process.env.LINA_TEXT_FIXTURE, mode=process.env.LINA_TEXT_MODE, adapter=process.env.LINA_TEXT_ADAPTER;
const file=n=>path.join(root,n), git=(...a)=>execFileSync('git',a,{encoding:'utf8'}).trim();
if(path.basename(process.argv[1])==='gh'){console.log(process.argv[2]==='api'?'[[]]':fs.readFileSync(file('pr.json'),'utf8'));process.exit(0);}
if(process.argv[2]==='--version'){console.log('codex-cli 99.0.0');process.exit(0);}
if(process.env.LINA_RESPONSES_API_KEY)throw Error('model credential leaked');
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x})), reply=(id,result)=>send({id,result});
const notify=(method,params)=>send({method,params});
let attempt=0,session='',turn='',text='',cwd='';
const history=()=>fs.existsSync(file('starts.json'))?JSON.parse(fs.readFileSync(file('starts.json'))):[];
const unframe=s=>s.replace(/^<lina-message [^>]*><text>/,'').replace(/<\/text><\/lina-message>$/,'').replace(/&(#x[0-9a-f]+|#[0-9]+|amp|lt|gt|quot|apos);/gi,(_,e)=>e[0]==='#'?String.fromCodePoint(e[1].toLowerCase()==='x'?parseInt(e.slice(2),16):Number(e.slice(1))):({amp:'&',lt:'<',gt:'>',quot:'"',apos:"'"})[e]);
function start(method,p){
 const rows=history();attempt=rows.length+1;if(attempt>2)throw Error('third automatic start');
 cwd=p.cwd;if(adapter==='acp'&&cwd!==process.cwd())throw Error('worktree moved');process.chdir(cwd);
 session='session-'+attempt;turn=adapter==='acp'?'acp:1':'turn-1';
 rows.push({Method:method,Session:session,Cwd:cwd});fs.writeFileSync(file('starts.json'),JSON.stringify(rows));
}
function prompt(packet){
 packet=unframe(packet);const d=JSON.parse(packet.slice(packet.indexOf('{')));
 const rows=history();
 if(attempt===1)git('switch','-c',d.branch);
 if(attempt>1&&fs.readFileSync('retained.txt','utf8')!=='retained')throw Error('partial work lost');
 rows.at(-1).Branch=git('branch','--show-current');rows.at(-1).Packet=packet;fs.writeFileSync(file('starts.json'),JSON.stringify(rows));
 if((mode==='restart'||mode==='incomplete'||mode==='second-failure')&&(attempt===1||mode==='second-failure')){
  fs.writeFileSync('retained.txt','retained');
  text=mode==='incomplete'?'[tool_use id=x name=coordinate] {"request":':'[tool_use id=x name=_bash] {"command":"pwd"}';return;
 }
 if(fs.existsSync('retained.txt'))fs.unlinkSync('retained.txt');
 fs.writeFileSync('result.txt','Recovered result\n');git('add','result.txt');git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Create result');
 const head=git('rev-parse','HEAD');
 fs.writeFileSync(file('pr.json'),JSON.stringify({number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:d.branch,baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:'COMPLETED',conclusion:'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
 if(attempt>1){text='Result committed.';return;}
 let args={};if(mode==='coordinate')args={request:'Text call coordinated'};
 if(mode==='_submit_receipt'){
  const task=packet.match(/Assignment: ([^\n]+)/)[1], outcome='blocked_needs_input';
  args={receipt:{schema_version:1,eventId:crypto.createHash('sha256').update([task,1,outcome,turn,1].join('|')).digest('hex').slice(0,32),relationshipId:task,executionGeneration:1,attempt:1,revisionHash:'0'.repeat(64),outcome,producer:'child',turnRef:{threadId:session,turnId:turn,turnStatus:'completed'},emittedAt:new Date().toISOString(),manifest:[],provenance:[]}};
 }
 text='[tool_use id=x name='+mode+'] '+JSON.stringify(args);
}
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line),p=m.params||{};
 switch(m.method){
 case 'initialize':reply(m.id,adapter==='acp'?{protocolVersion:1,agentInfo:{name:'fixture',version:'99.0.0'},agentCapabilities:{loadSession:true,sessionCapabilities:{resume:{}}}}:{userAgent:'fixture/99.0.0',platformFamily:'unix',platformOs:'linux'});break;
 case 'initialized':break;
 case 'account/rateLimits/read':reply(m.id,{rateLimits:{}});break;
 case 'session/new':case 'thread/start':
  start(m.method,p);
  if(adapter==='codex'&&JSON.stringify(p.dynamicTools).indexOf('submit_receipt')<0)throw Error('work tools undeclared');
  reply(m.id,adapter==='acp'?{sessionId:session}:{thread:{id:session},cwd,model:'fixture'});break;
 case 'session/load':case 'session/resume':case 'thread/resume':throw Error('contaminated session resumed');
 case 'session/prompt':
  prompt(p.prompt[0].text);
  for(const part of [text.slice(0,5),text.slice(5)])notify('session/update',{sessionId:session,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:part}}});
  reply(m.id,{stopReason:mode==='incomplete'&&attempt===1?'stop':'end_turn'});break;
 case 'turn/start':
  prompt(p.input[0].text);
  // Exercise completion before the start reply, without turn/started.
  for(const delta of [text.slice(0,5),text.slice(5)])notify('item/agentMessage/delta',{threadId:session,turnId:turn,itemId:'message',delta});
  notify('item/completed',{threadId:session,turnId:turn,item:{type:'agentMessage',id:'message',text}});
  notify('turn/completed',{threadId:session,turn:{id:turn,status:'completed',items:[{type:'agentMessage',id:'message',text}]}});
  reply(m.id,{turn:{id:turn}});break;
 case 'thread/read':reply(m.id,{thread:{id:session,status:{type:'idle'},turns:[{id:turn,status:'completed'}]}});break;
 case 'thread/turns/list':reply(m.id,{data:[{id:turn,status:'completed',items:[{type:'agentMessage',id:'message',text}]}]});break;
 default:throw Error('unexpected method '+m.method);
 }
});
`
