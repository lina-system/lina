// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
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

// Real Core receives an authenticated owner follow-up; the synthetic worker
// consumes it through Core's MCP server, changes a file, and reports its ID.
func TestWorkerSteeringThroughRunningCore(t *testing.T) {
	script := strings.Replace(acpAgentScript, "import {execFileSync}", "import {spawn,execFileSync}", 1)
	script = strings.Replace(script, "let pending,packet;", steeringMCP+"\nlet pending,packet,bridge;", 1)
	script = strings.Replace(script, "readline.createInterface({input:process.stdin}).on('line',line=>{", "readline.createInterface({input:process.stdin}).on('line',async line=>{", 1)
	script = strings.Replace(script, "fs.writeFileSync(path.join(root,'cwd'),process.cwd());reply", "bridge=mcp(m.params.mcpServers[0]);await bridge.call('initialize',{protocolVersion:'2025-06-18'});fs.writeFileSync(path.join(root,'cwd'),process.cwd());reply", 1)
	script = strings.Replace(script, "release(()=>{", "release(async ()=>{", 1)
	script = strings.Replace(script, "const definition=JSON.parse(messageText(packet).slice(messageText(packet).indexOf('{')));", `const response=await bridge.call('tools/call',{name:'read_projection',arguments:{}});
  const inbox=response.result.content.map(c=>JSON.parse(messageText(c.text))).find(c=>c.steering);
  if(!inbox||inbox.steering.length!==1)throw Error('missing steering in next tool result');
  const mail=inbox.steering[0];
  await bridge.call('tools/call',{name:'check_inbox',arguments:{read:[mail.id]}});
  fs.writeFileSync(path.join(root,'steering-id'),mail.id);
  const definition=JSON.parse(messageText(packet).slice(messageText(packet).indexOf('{')));`, 1)
	script = strings.Replace(script, "fs.writeFileSync('result.txt','ACP result\\n')", "fs.writeFileSync('result.txt',messageText(mail.text)+'\\n')", 1)
	script = strings.Replace(script, "reply(pending,{stopReason:'end_turn'});", `await bridge.call('tools/call',{name:'check_inbox',arguments:{acted:[mail.id]}});
  await bridge.close();reply(pending,{stopReason:'end_turn'});`, 1)
	runACPWorkersThroughRunningCore(t, []acpScenario{{name: "steering", worker: "gemini", binary: "gemini", mode: "steering", script: script,
		beforeFinish: func(t *testing.T, h *harness, fixture string, record dispatch.Record) {
			client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "steering-owner", []protocol.Capability{{Name: "running", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			id, err := client.Control(h.ctx, protocol.FollowUp{RunID: "task:" + record.ID + ":1", Text: "Result amended by steering"})
			if err != nil {
				t.Fatal(err)
			}
			for {
				frame, err := client.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if frame.ReplyTo != nil && *frame.ReplyTo == id {
					if frame.Error != nil {
						t.Fatal(frame.Error)
					}
					break
				}
			}
		},
		afterFinish: func(t *testing.T, h *harness, fixture string, record dispatch.Record) {
			if record.State != "finished" || record.Code != "ready_for_review" || record.PR == nil {
				t.Fatal("steered work did not finish", record)
			}
			raw, err := os.ReadFile(filepath.Join(h.root, "work/tasks", record.ID, "result.txt"))
			if err != nil || string(raw) != "Result amended by steering\n" {
				t.Fatalf("steered result: %q %v", raw, err)
			}
			db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(context.Background())
			messages, err := work.New(db).SteeringMessages(h.ctx, "gemini:acp-fixture")
			if err != nil || len(messages) != 1 || messages[0].State != "read" || messages[0].ActionLatencyMS == nil || messages[0].Tier != "turn" {
				t.Fatalf("mailbox receipts: %+v %v", messages, err)
			}
			receipts, err := work.New(db).SteeringReceipts(h.ctx, messages[0].ID)
			if err != nil || len(receipts) != 5 {
				t.Fatalf("receipts: %+v %v", receipts, err)
			}
			card, err := work.New(db).Get(h.ctx, record.ID)
			if err != nil || len(card.Steering) != 1 || card.Steering[0].ActedAt == nil {
				t.Fatal("work card lost receipt", card, err)
			}
			t.Logf("turn send-to-action: %d ms (worker report); delivered once; committed result verified", *messages[0].ActionLatencyMS)
			if messages[0].ActedAt.Before(messages[0].SentAt) || messages[0].ActedAt.After(time.Now()) {
				t.Fatal("invalid measurement")
			}
		}}})
}

const steeringMCP = `
function mcp(server){
 const env=Object.fromEntries(server.env.map(e=>[e.name,e.value]));
 const child=spawn(server.command,server.args,{env,stdio:['pipe','pipe','inherit']});
 let seq=0;const pending=new Map();
 readline.createInterface({input:child.stdout}).on('line',line=>{const r=JSON.parse(line),p=pending.get(r.id);if(p){pending.delete(r.id);if(r.error||r.result?.isError)p.reject(Error(JSON.stringify(r)));else p.resolve(r)}});
 child.on('error',e=>{for(const p of pending.values())p.reject(e)});
 child.on('exit',()=>{for(const p of pending.values())p.reject(Error('MCP exited'))});
 return {call:(method,params={})=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});child.stdin.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n')}),close:()=>new Promise(resolve=>{child.on('exit',resolve);child.stdin.end()})};
}
`
