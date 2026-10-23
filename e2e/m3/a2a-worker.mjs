#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Controlled installed-worker peer. Its filesystem effects and approval wire
// are real; it uses no model account, remote repository or private credential.
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
const root=process.env.LINA_WORKER_FIXTURE;
const mode=process.env.LINA_WORKER_CASE;
const send=value=>console.log(JSON.stringify(value));
if(process.argv[2]==='--version') {
  console.log(mode==='denied'?'codex-cli 99.0.0':'agy fixture-1');
  process.exit(0);
}
if(mode==='uncertain-cancel') {
  fs.readFileSync(0,'utf8');
  fs.appendFileSync(path.join(root,'launches'),'worker\n');
  send({event:'init',conversation_id:'uncertain',init:{cwd:process.cwd(),permission_mode:'request-review'}});
  fs.writeFileSync('result.txt','partial effect before cancellation\n');
  send({event:'step_update',step_update:{conversation_id:'uncertain',step_index:1,state:'DONE',tool_name:'run_command'}});
  fs.writeFileSync(path.join(root,'effect-observed'),'1');
  // No terminal result is sent. Stopping this process cannot prove whether its
  // earlier file operation satisfied the task or that its effects were undone.
  setInterval(()=>{},1000);
  await new Promise(()=>{});
}
let cwd;
let status='inProgress';
for await(const line of readline.createInterface({input:process.stdin})) {
  const request=JSON.parse(line),p=request.params??{};
  const reply=result=>send({id:request.id,result});
  switch(request.method) {
  case 'initialize':reply({userAgent:'fixture'});break;
  case 'initialized':break;
  case 'account/rateLimits/read':reply({rateLimits:{}});break;
  case 'thread/start':cwd=p.cwd;reply({thread:{id:'approval-thread'},cwd,model:'fixture'});break;
  case 'turn/start':
    fs.appendFileSync(path.join(root,'launches'),'worker\n');
    reply({turn:{id:'approval-turn',status:'inProgress'}});
    send({method:'turn/started',params:{threadId:'approval-thread',turn:{id:'approval-turn',status:'inProgress'}}});
    send({id:900,method:'item/commandExecution/requestApproval',params:{threadId:'approval-thread',turnId:'approval-turn',itemId:'write-result',startedAtMs:1800000000000,command:'write result.txt',cwd,availableDecisions:['accept','decline']}});
    break;
  case undefined:
    if(request.id!==900||request.result?.decision!=='decline')throw Error('expected an owner denial');
    fs.writeFileSync(path.join(root,'owner-denied'),'1');
    send({method:'serverRequest/resolved',params:{threadId:'approval-thread',requestId:900}});
    status='interrupted';
    send({method:'turn/completed',params:{threadId:'approval-thread',turn:{id:'approval-turn',status}}});
    break;
  case 'thread/read':reply({thread:{id:'approval-thread',cwd,status:{type:status==='inProgress'?'active':'idle'}}});break;
  case 'thread/turns/list':reply({data:[{id:'approval-turn',status,itemsView:'full',items:[]}]});break;
  case 'turn/interrupt':reply({});break;
  default:throw Error(`unexpected request ${request.method}`);
  }
}
