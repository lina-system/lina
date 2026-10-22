// SPDX-License-Identifier: Apache-2.0
// Synthetic installed worker and forge; actual Core, Git and task worktrees run.
import { createInterface } from 'node:readline';
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { join, basename } from 'node:path';
import { execFileSync } from 'node:child_process';
const [root, mode] = process.argv.slice(2);
const cfg = JSON.parse(readFileSync(join(root, 'fixture.json')));
const git = (cwd, ...args) => execFileSync(cfg.git, ['-C', cwd, ...args], {encoding:'utf8'}).trim();
if (mode === '--version') { process.stdout.write('codex-cli 99.0.0\n'); } else if (mode === 'forge') {
  const branch = process.argv[6]; // root forge pr view <branch>
  const task = branch.slice('lina/'.length);
  const head = git(cfg.clone, 'rev-parse', branch);
  const merged = existsSync(join(root, `${task}.merged`));
  process.stdout.write(JSON.stringify({number: 1, headRefOid:head, headRefName:branch, baseRefName:'dev', state:merged?'MERGED':'OPEN', mergeable:'MERGEABLE', mergeCommit:merged?{oid:git(cfg.clone,'rev-parse','dev')}:null}));
} else {
  const threads = new Map();
  const send = value => process.stdout.write(`${JSON.stringify(value)}\n`);
  createInterface({input:process.stdin}).on('line', line => {
    const message = JSON.parse(line);
    const {id,method,params:p} = message;
    let result = {};
    switch (method) {
      case 'initialized': return;
      case 'initialize': result={userAgent:'synthetic-codex',codexHome:root,platformFamily:'unix',platformOs:'linux'};break;
      case 'account/rateLimits/read':result={rateLimits:{}};break;
      case 'thread/start': {
        const thread={id:basename(p.cwd),cwd:p.cwd,status:{type:'idle'},turns:[],count:0};threads.set(thread.id,thread);result={thread,cwd:p.cwd,model:'fixture-model'};break;
      }
      case 'thread/read': result={thread:threads.get(p.threadId)};break;
      case 'turn/start': {
        const thread=threads.get(p.threadId);thread.count++;
        const definition=JSON.parse(p.input[0].text.split('Work definition:\n')[1]);
        const folder=definition.key==='c'?'independent':'shared';
        if (definition.key==='b'&&!existsSync(join(thread.cwd,'shared','a.txt'))) throw new Error('dependent started before landed base');
        mkdirSync(join(thread.cwd,folder),{recursive:true});writeFileSync(join(thread.cwd,folder,`${definition.key}.txt`),'verified fixture result\n');
        git(thread.cwd,'add','.');git(thread.cwd,'-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m',`fixture ${definition.key}`);
        writeFileSync(join(root,`${thread.id}.started`),String(thread.count));
        const turn={id:`turn-${thread.count}`,status:'completed',items:[]};thread.turns.push(turn);
        send({method:'turn/started',params:{threadId:thread.id,turn:{...turn,status:'inProgress'}}});
        send({id,result:{turn:{...turn,status:'inProgress'}}});
        send({method:'turn/completed',params:{threadId:thread.id,turn}});return;
      }
      default: send({id,error:{code:-32601,message:'unsupported fixture method'}});return;
    }
    send({id,result});
  });
}
