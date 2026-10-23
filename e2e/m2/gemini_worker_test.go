// SPDX-License-Identifier: Apache-2.0
package e2e_test

import "testing"

// The native Gemini fixture uses the real install and Core dispatch paths.
// Sign-in refusal reports Gemini and hands off to the next installed ACP worker.
func TestGeminiReadinessThroughRunningCore(t *testing.T) {
	runACPReadinessThroughRunningCore(t, acpReadinessScenario{
		worker: "gemini", binary: "gemini", version: "0.65.0",
		fallback: "grok", fallbackVersion: "grok-fixture-753", script: geminiAgentScript,
	})
}

const geminiAgentScript = `#!/usr/bin/env node
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
if(process.argv[2]==='--version') { trace('--version'); console.log(name==='gemini'?'not-the-handshake':'grok-fixture-753'); process.exit(0); }
if(JSON.stringify(process.argv.slice(2))!==JSON.stringify(name==='gemini'?['--acp']:['--no-auto-update','agent','stdio'])) throw Error('unexpected arguments');
const send=x=>console.log(JSON.stringify({jsonrpc:'2.0',...x}));
const reply=(id,result)=>send({id,result});
const session=name+'-session';
readline.createInterface({input:process.stdin}).on('line',line=>{
 const m=JSON.parse(line);trace(m.method);
 switch(m.method){
 case 'initialize':
  if(m.params.protocolVersion!==1||m.params.clientCapabilities.fs||m.params.clientCapabilities.terminal)throw Error('unexpected capabilities');
  reply(m.id,{protocolVersion:1,agentCapabilities:{loadSession:true},...(name==='gemini'?{agentInfo:{name:'gemini-cli',version:'0.65.0'}}:{})});break;
 case 'session/new':
  if(m.params.cwd!==process.cwd()||m.params.mcpServers.length!==1||m.params.mcpServers[0].name!=='lina'||m.params.mcpServers[0].args[0]!=='worker-tools')throw Error('unexpected session setup');
  if(name==='gemini'&&process.env.LINA_ACP_CASE==='needs-sign-in') {
   send({id:m.id,error:{code:-32000,message:'Authentication required',data:{private:'account-detail-must-stay-private'}}});break;
  }
  reply(m.id,{sessionId:session});break;
 case 'session/prompt': {
  if(name==='gemini'&&process.env.LINA_ACP_CASE==='needs-sign-in')throw Error('prompt without sign-in');
  const packet=m.params.prompt[0].text;
  send({method:'session/update',params:{sessionId:session,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Working on result.txt'}}}});
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
