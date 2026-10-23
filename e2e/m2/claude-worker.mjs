#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Synthetic vendor/GitHub boundary; all work and commits happen in Core's worktree.
function messageText(text) {
 if (!text.startsWith('<lina-message ')) return text;
 const match=text.match(/^<lina-message [^>]*><text>([\s\S]*)<\/text><\/lina-message>$/);
 if (!match) throw Error('invalid worker message frame');
 return match[1].replace(/&(#x[0-9a-f]+|#[0-9]+|amp|lt|gt|quot|apos);/gi,(_,e)=>e[0]==='#'?String.fromCodePoint(e[1].toLowerCase()==='x'?parseInt(e.slice(2),16):Number(e.slice(1))):({amp:'&',lt:'<',gt:'>',quot:'"',apos:"'"})[e]);
}
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
} else if (args[0] === '--version') console.log('Claude Code fixture-1');
else {
  if (args.join(' ') !== '--print --output-format stream-json --verbose') throw Error('unexpected settings override');
  if (process.env.LINA_RESPONSES_API_KEY) throw Error('Core provider credential leaked');
  const prompt = messageText(fs.readFileSync(0, 'utf8'));
  if (!prompt.includes('result.txt')) throw Error('task packet lost');
  console.log(JSON.stringify({type:'system',subtype:'init',session_id:'fixture',cwd:process.cwd(),permissionMode:'default'}));
  const counter = path.join(root, 'attempt-count');
  const attempts = fs.existsSync(counter) ? Number(fs.readFileSync(counter, 'utf8')) + 1 : 1;
  fs.writeFileSync(counter, String(attempts));
  if (attempts > 1 && !prompt.includes('Do not recreate or clean the worktree')) throw Error('recovery lost continuation');
  if (mode === 'exit-zero') process.exit(0);
  const malformed = {
    invalid_message: '{',
    session_mismatch: JSON.stringify({type:'assistant',session_id:'foreign'}),
    invalid_result: JSON.stringify({type:'result',session_id:'fixture',subtype:'success'}),
    invalid_init: JSON.stringify({type:'system',subtype:'init',session_id:'fixture',cwd:process.cwd()}),
    invalid_usage: JSON.stringify({type:'result',session_id:'fixture',subtype:'success',is_error:false,usage:{input_tokens:-1}}),
  };
  if (Object.hasOwn(malformed, mode)) {
    console.log(malformed[mode]);
    process.exit(0);
  }
  if (mode === 'denied') {
    console.log(JSON.stringify({type:'result',subtype:'success',session_id:'fixture',is_error:false,result:'Permission needed.',permission_denials:[{tool_name:'Bash',tool_use_id:'tool'}]}));
  } else {
    const git = (...args) => execFileSync('git',args,{encoding:'utf8'}).trim();
    const task = prompt.startsWith('{') ? JSON.parse(prompt).task_packet : prompt;
    const definition = JSON.parse(task.slice(task.indexOf('{')));
    const allocation = {Base:git('rev-parse','HEAD'), Detached:git('branch','--show-current') === '', Branch:definition.branch};
    if (!allocation.Detached) throw Error('worker was not given a detached base');
    git('switch','-c',definition.branch);
    fs.writeFileSync('result.txt', 'worker produced this result\n');
    git('add','result.txt');
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
    fs.writeFileSync(path.join(root,'pr.json'),JSON.stringify({number:17,url:'https://github.com/fixture/project/pull/17',state:'OPEN',isDraft:false,headRefName:git('branch','--show-current'),baseRefName:'dev',headRefOid:head,statusCheckRollup:[{__typename:'CheckRun',name:'foundation',status:mode==='pending'?'IN_PROGRESS':'COMPLETED',conclusion:mode==='pending'?'':'SUCCESS'}],reviews:[{state:'COMMENTED',body:'No blocking findings.',commit:{oid:head}}]}));
    console.log(JSON.stringify({type:'assistant',session_id:'fixture',message:{id:'message',content:[{type:'tool_use',id:'tool',name:'Bash',input:{command:'git status'}}]}}));
    console.log(JSON.stringify({type:'result',subtype:'success',session_id:'fixture',is_error:false,result:'Work submitted.',permission_denials:[]}));
    setInterval(() => {}, 1000);
  }
}
