// SPDX-License-Identifier: Apache-2.0
// Synthetic installed worker and forge; actual Core, Git and task worktrees run.
import { createHash } from 'node:crypto';
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
  const definition = JSON.parse(readFileSync(join(root, `${task}.definition.json`)));
  const mergeable = cfg.conflict && definition.key === 'a' ? cfg.conflict.mergeable : 'MERGEABLE';
  process.stdout.write(JSON.stringify({number: 1, headRefOid:head, headRefName:branch, baseRefName:'dev', state:merged?'MERGED':'OPEN', mergeable, mergeCommit:merged?{oid:git(cfg.forge,'rev-parse','dev')}:null}));
} else {
  writeFileSync(join(root,'worker.pid'),String(process.pid));
  const threads = new Map();
  const send = value => process.stdout.write(`${JSON.stringify(value)}\n`);
  const hash = text => createHash('sha256').update(text).digest('hex');
  const callTool = (thread, name, args) => send({id:`${thread.id}:${name}`,method:'item/tool/call',params:{threadId:thread.id,turnId:thread.turn.id,callId:name,namespace:'lina',tool:name,arguments:args}});
  const finish = thread => {
        const definition=thread.definition;
        const folder=definition.key==='c'?'independent':'shared';
        if (definition.key==='b'&&!existsSync(join(thread.cwd,'shared','a.txt'))) throw new Error('dependent started before landed base');
        mkdirSync(join(thread.cwd,folder),{recursive:true});writeFileSync(join(thread.cwd,folder,`${definition.key}.txt`),'verified fixture result\n'+(thread.count>1?'conflict corrected\n':''));
        git(thread.cwd,'add','.');git(thread.cwd,'-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-m',`fixture ${definition.key}`);
        git(thread.cwd,'push','origin',`HEAD:refs/heads/lina/${thread.id}`);
        writeFileSync(join(root,`${thread.id}.started`),String(thread.count));
        const path=join(thread.cwd,folder,`${definition.key}.txt`), sha256=hash(readFileSync(path));
        const revision=hash(`${path}:${sha256}`), emitted=new Date().toISOString();
        const receipt={schema_version:1,relationshipId:thread.id,executionGeneration:1,attempt:1,revisionHash:revision,outcome:'ready_for_review',producer:'child',turnRef:{threadId:thread.id,turnId:thread.turn.id,turnStatus:'completed'},emittedAt:emitted,manifest:[{path,sha256}],provenance:[{artifact_id:`artifact-${definition.key}`,task_id:thread.id,node_id:thread.projection.node_id,path,sha256,observed_at:emitted}]};
        receipt.eventId=hash(`${thread.id}|1|${revision}|ready_for_review`).slice(0,32);
        thread.receipt=receipt;
        callTool(thread,'submit_receipt',{receipt});
  };
  createInterface({input:process.stdin}).on('line', line => {
    const message = JSON.parse(line);
    const {id,method,params:p} = message;
    if (!method) {
      const [threadID,kind]=String(id).split(':');const thread=threads.get(threadID);
      if(['read_projection','ask','coordinate','submit_receipt'].includes(kind)) {
        if(!message.result?.success)throw new Error(`work tool failed: ${kind} ${JSON.stringify(message)}`);
        const text=message.result.contentItems.find(item=>item.type==='inputText')?.text;
        const result=JSON.parse(text);
        if(kind==='read_projection') {
          if(result.assignment_id!==thread.id||!result.task_packet.includes('Work definition:'))throw new Error('foreign projection');
          thread.projection=result;
          if(cfg.requests&&thread.definition.key==='a')callTool(thread,'ask',{question:'Confirm the work-tool result.'});
          else callTool(thread,'coordinate',{request:'Record this scheduled result.'});
        } else if(kind==='ask') {
          if(result.answer!=='confirmed')throw new Error('work-tool answer not delivered');
          send({id:`${thread.id}:question`,method:'item/tool/requestUserInput',params:{threadId:thread.id,turnId:thread.turn.id,itemId:'question',questions:[{id:'answer',header:'Fixture',question:'Confirm the scheduled result.'}]}});
        } else if(kind==='coordinate') {
          if(!result.accepted||!result.event_id)throw new Error('coordination not recorded');
          finish(thread);
        } else {
          if(!result.accepted||result.verified!==false||!result.event_id.endsWith(thread.receipt.eventId))throw new Error('receipt not retained as unverified evidence');
          writeFileSync(join(root,`${thread.id}.receipt.json`),JSON.stringify(thread.receipt));
          thread.turn.status='completed';thread.status={type:'idle'};
          send({method:'turn/completed',params:{threadId:thread.id,turn:thread.turn}});
        }
      } else if(kind==='question') {
        if(message.result?.answers?.answer?.answers?.[0]!=='confirmed')throw new Error('question answer did not reach worker');
        send({id:`${thread.id}:approval`,method:'item/commandExecution/requestApproval',params:{threadId:thread.id,turnId:thread.turn.id,itemId:'write',startedAtMs:1800000000000,command:'write scheduled artifact',cwd:thread.cwd,availableDecisions:['accept','decline']}});
      } else if(kind==='approval') {
        if(message.result?.decision!=='accept')throw new Error('approval did not reach worker');
        callTool(thread,'coordinate',{request:'Record this scheduled result.'});
      } else throw new Error('unbound worker reply');
      return;
    }
    if ((cfg.fail==='handshake'&&method==='initialize')||(cfg.fail==='quota'&&method==='account/rateLimits/read')) { send({id,error:{code:-32000,message:'synthetic readiness failure'}});return; }
    let result = {};
    switch (method) {
      case 'initialized': return;
      case 'initialize': result={userAgent:'synthetic-codex',codexHome:root,platformFamily:'unix',platformOs:'linux'};break;
      case 'account/rateLimits/read':result={rateLimits:{}};break;
      case 'thread/start': {
        const names=(p.dynamicTools||[]).flatMap(spec=>spec.name==='lina'?(spec.tools||[]).map(tool=>tool.name):[]);
        for(const name of ['read_projection','ask','coordinate','submit_receipt'])if(!names.includes(name))throw new Error(`missing work tool ${name}`);
        const thread={id:basename(p.cwd),cwd:p.cwd,status:{type:'idle'},turns:[],count:0};threads.set(thread.id,thread);result={thread,cwd:p.cwd,model:'fixture-model'};break;
      }
      case 'thread/read': result={thread:threads.get(p.threadId)};break;
      case 'turn/start': {
        const thread=threads.get(p.threadId);thread.count++;
        if(thread.count===1) {
          thread.definition=JSON.parse(p.input[0].text.split('Work definition:\n')[1]);
          writeFileSync(join(root,`${thread.id}.definition.json`),JSON.stringify(thread.definition));
        } else if(!cfg.conflict||thread.count!==cfg.conflict.returns+1||!p.input[0].text.includes('unmergeable'))throw new Error('unexpected conflict return');
        thread.turn={id:`turn-${thread.count}`,status:'inProgress',items:[]};
        thread.turns.push(thread.turn);thread.status={type:'active'};
        send({method:'turn/started',params:{threadId:thread.id,turn:{...thread.turn,status:'inProgress'}}});
        send({id,result:{turn:{...thread.turn,status:'inProgress'}}});
        callTool(thread,'read_projection',{});
        return;
      }
      default: send({id,error:{code:-32601,message:'unsupported fixture method'}});return;
    }
    send({id,result});
  });
}
