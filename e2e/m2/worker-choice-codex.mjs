#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Synthetic Codex wire boundary; the task's Git/filesystem effects are real.
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import {execFileSync} from 'node:child_process';
const root=process.env.LINA_WORKER_FIXTURE;
const mode=process.env.LINA_WORKER_CASE;
if(process.argv[2]==='--version') {console.log('codex-cli 99.0.0');process.exit(0);}
let cwd;
const send=v=>console.log(JSON.stringify(v));
for await (const line of readline.createInterface({input:process.stdin})) {
 const request=JSON.parse(line), p=request.params??{};
 const reply=result=>send({id:request.id,result});
 switch(request.method) {
 case 'initialize':reply({userAgent:'fixture'});break;
 case 'initialized':break;
 case 'account/rateLimits/read':
  if(mode==='quota-windows') {reply({accountId:'fixture-account',rateLimits:{}});break;}
  reply({rateLimits: mode==='quota-fallback' || mode==='double-quota' || mode==='rule' ? {} : {primary:{usedPercent:100,windowDurationMins:60,resetsAt:Math.floor(Date.now()/1000)+3600}}});break;
 case 'thread/start':
  cwd=p.cwd;
  reply({thread:{id:'choice-codex'},cwd,model:'fixture'});break;
 case 'turn/start': {
  const packet=p.input[0].text;
  if(fs.existsSync(path.join(root,'first-pid'))) {
   const pid=Number(fs.readFileSync(path.join(root,'first-pid'),'utf8'));
   let alive=false;try {process.kill(pid,0);alive=true;}catch{}
   if(alive) throw Error('replacement overlaps first process');
  }
  if(mode==='quota-fallback' && fs.readFileSync(path.join(cwd,'result.txt'),'utf8')!=='partial work\n') throw Error('partial work lost');
  fs.appendFileSync(path.join(root,'attempts'),'codex\n');
  fs.writeFileSync(path.join(root,'second-packet'),packet);
  send({method:'turn/started',params:{threadId:'choice-codex',turn:{id:'choice-turn',status:'inProgress'}}});
  reply({turn:{id:'choice-turn',status:'inProgress'}});
  if(mode==='quota-windows') {
   send({method:'account/rateLimits/updated',params:{rateLimits:{primary:{usedPercent:100,windowDurationMins:300,resetsAt:Math.floor(Date.now()/1000)+3600},secondary:{usedPercent:25,windowDurationMins:10080,resetsAt:Math.floor(Date.now()/1000)+604800}}}});
   send({method:'turn/completed',params:{threadId:'choice-codex',turn:{id:'choice-turn',status:'failed',error:{message:'quota',codexErrorInfo:'usageLimitExceeded'}}}});
   break;
  }
  if(mode==='double-quota') {
   send({method:'turn/completed',params:{threadId:'choice-codex',turn:{id:'choice-turn',status:'failed',error:{message:'quota',codexErrorInfo:'usageLimitExceeded'}}}});
   break;
  }
  // Reuse the synthetic worker's real commit/PR production with its stdout
  // captured; only the Codex envelopes below cross this adapter boundary.
  execFileSync(path.join(path.dirname(process.argv[1]),'agy'),['--output-format','stream-json'],{cwd,input:packet,env:{...process.env,LINA_WORKER_CASE:'success'},stdio:['pipe','pipe','pipe']});
  send({method:'turn/completed',params:{threadId:'choice-codex',turn:{id:'choice-turn',status:'completed'}}});break;
 }
 default:throw Error(`unexpected request ${request.method}`);
 }
}
