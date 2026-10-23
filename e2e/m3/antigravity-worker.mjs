#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Synthetic vendor/GitHub boundary; all work and commits happen in Core's worktree.
import fs from 'node:fs';
import path from 'node:path';
import {execFileSync, spawnSync} from 'node:child_process';
const args = process.argv.slice(2);
const root = process.env.LINA_WORKER_FIXTURE;
const mode = process.env.LINA_WORKER_CASE;
if (path.basename(process.argv[1]) === 'gh') {
  if (args[0] === 'pr' && args[1] === 'create') {
    fs.writeFileSync(path.join(root, 'created'), '1');
    console.log('https://github.com/fixture/project/pull/17');
  } else if (args[0] === 'api') console.log('[[]]');
  else console.log(fs.readFileSync(path.join(root, 'pr.json'), 'utf8'));
} else if (args[0] === '--version') console.log('agy fixture-1');
else {
  fs.appendFileSync(path.join(root, 'launches'), 'worker\n');
  if (args.join(' ') !== '--output-format stream-json') throw Error('unexpected settings override');
  if (process.env.LINA_RESPONSES_API_KEY) throw Error('Core provider credential leaked');
  const prompt = fs.readFileSync(0, 'utf8');
  fs.writeFileSync(path.join(root, 'packet'), prompt);
  if (!prompt.includes('result.txt')) throw Error('task packet lost');
  console.log(JSON.stringify({event:'init',conversation_id:'fixture',init:{cwd:process.cwd(),permission_mode:'request-review'}}));
  if (mode === 'quota-fallback' || mode === 'double-quota') {
    fs.appendFileSync(path.join(root,'attempts'),'antigravity\n');
    fs.writeFileSync(path.join(root,'first-packet'),prompt);
    if(mode==='quota-fallback') fs.writeFileSync('result.txt','partial work\n');
    // Publish the observation before the terminal event can trigger Close.
    fs.writeFileSync(path.join(root,'first-pid'),String(process.pid));
    console.log(JSON.stringify({event:'result',result:{conversation_id:'fixture',status:'ERROR',error:'RESOURCE_EXHAUSTED'}}));
    // Keep the process alive to prove Core reaps it before the replacement.
    setInterval(()=>{},1000);
    await new Promise(()=>{});
  }
  if(mode==='overlap') {
    const deadline=Date.now()+60000;
    while(!fs.existsSync(path.join(root,'release-first'))) {
      if(Date.now()>deadline) throw Error('Core did not confirm the area wait');
      await new Promise(resolve=>setTimeout(resolve,20));
    }
  }
  if (mode === 'exit-zero') process.exit(0);
  if (mode === 'denied') {
    console.log(JSON.stringify({event:'result',result:{conversation_id:'fixture',status:'SUCCESS',denied_actions:[{action:'command',display_name:'RunCommand'}]}}));
  } else {
    const git = (...args) => execFileSync('git',args,{encoding:'utf8'}).trim();
    const task = prompt.startsWith('{') ? JSON.parse(prompt).task_packet : prompt;
    const definition = JSON.parse(task.slice(task.indexOf('{')));
    const allocation = {Base:git('rev-parse','HEAD'), Detached:git('branch','--show-current') === '', Branch:definition.branch};
    if (!allocation.Detached) throw Error('worker was not given a detached base');
    git('switch','-c',definition.branch);
    const partial=fs.existsSync('result.txt')?fs.readFileSync('result.txt','utf8'):'';
    fs.writeFileSync('result.txt', partial+'worker produced this result\n');
    git('add','result.txt');
    if (mode === 'scope') { fs.writeFileSync('outside.txt','outside declared area\n'); git('add','outside.txt'); }
    git('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m','Produce result');
    execFileSync('gh',['pr','create']);
    const head=git('rev-parse','HEAD');
    allocation.Protected = ['refs/heads/dev', 'refs/heads/main', 'refs/heads/master', 'refs/heads/another-task', 'refs/remotes/origin/dev', `refs/lina/base/${path.basename(process.cwd())}`].every(ref => {
      const attempt = spawnSync('git',['update-ref',ref,head],{encoding:'utf8'});
      return attempt.status !== 0 && attempt.stderr.includes('LINA protected ref');
    });
    if (!allocation.Protected) throw Error('worker changed a protected ref');
    fs.writeFileSync(path.join(root,'allocation.json'),JSON.stringify(allocation));
    fs.writeFileSync(path.join(root,'workdir'),process.cwd());
    fs.writeFileSync(path.join(root,'pr.json'),JSON.stringify({number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:git('branch','--show-current'),baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:mode==='pending'?'IN_PROGRESS':'COMPLETED',conclusion:mode==='pending'?'':mode==='failed-check'?'FAILURE':'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
    console.log(JSON.stringify({event:'step_update',step_update:{conversation_id:'fixture',step_index:1,state:'DONE',tool_name:'run_command'}}));
    console.log(JSON.stringify({event:'result',result:{conversation_id:'fixture',status:'SUCCESS',response:'Work submitted.'}}));
  }
}
