#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Persisted worker observations are synthetic; worktree, file and commit are real.
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import {execFileSync} from 'node:child_process';
const root=process.env.LINA_WORKER_FIXTURE;
const args=process.argv.slice(2);
if(path.basename(process.argv[1])==='gh') {
 if(args[0]==='pr'&&args[1]==='create'){fs.writeFileSync(path.join(root,'created'),'1');console.log('https://github.com/fixture/project/pull/17');}
 else if(args[0]==='api') console.log('[[]]');
 else console.log(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
 process.exit(0);
}
if(args[0]==='--version'){console.log('codex-cli 99.0.0');process.exit(0);}
let cwd='',resumed=false;
const send=v=>console.log(JSON.stringify(v));
for await(const line of readline.createInterface({input:process.stdin})){
 const r=JSON.parse(line),p=r.params??{};
 const reply=result=>send({id:r.id,result});
 switch(r.method){
 case 'initialize':reply({userAgent:'recovery-fixture'});break;
 case 'initialized':break;
 case 'account/rateLimits/read':reply({rateLimits:{}});break;
 case 'thread/start':
  if(fs.existsSync(path.join(root,'attempts')))throw Error('recovery started another thread');
  cwd=p.cwd;reply({thread:{id:'dispatch-thread'},cwd,model:'fixture'});break;
 case 'turn/start':
  if(fs.existsSync(path.join(root,'attempts')))throw Error('recovery started another turn');
  fs.writeFileSync(path.join(root,'attempts'),'codex\n');
  reply({turn:{id:'dispatch-turn',status:'inProgress'}});
  execFileSync(path.join(path.dirname(process.argv[1]),'artifact-worker'),['--output-format','stream-json'],{cwd,input:p.input[0].text,env:{...process.env,LINA_WORKER_CASE:'success'},stdio:['pipe','pipe','pipe']});
  fs.writeFileSync(path.join(root,'worker-ready'),'ready');
  break; // Core is killed before a completion signal reaches it.
 case 'thread/resume':
  if(Object.keys(p).join(',')!=='threadId'||p.threadId!=='dispatch-thread')throw Error('recovery changed settings or identity');
  resumed=true;cwd=fs.readFileSync(path.join(root,'workdir'),'utf8');
  reply({thread:{id:'dispatch-thread'},cwd,model:'fixture'});break;
 case 'thread/read':reply({thread:{id:'dispatch-thread',cwd,status:{type:resumed?'idle':'active'}}});break;
 case 'thread/turns/list':
  fs.writeFileSync(path.join(root,'snapshot-requested'),'read');
  while(!fs.existsSync(path.join(root,'snapshot-release')))await new Promise(resolve=>setTimeout(resolve,5));
  reply({data:[{id:'dispatch-turn',status:'completed',itemsView:'full',items:[{type:'agentMessage',id:'result',text:'Work submitted.'}]}]});break;
 default:throw Error(`unexpected request ${r.method}`);
 }
}
