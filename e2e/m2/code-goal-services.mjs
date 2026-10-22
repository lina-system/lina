#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Local vendor/service fixtures; the installed Core owns every driver action.
import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
const args=process.argv.slice(2);
const name=path.basename(process.argv[1]);
const root=process.env.LINA_WORKER_FIXTURE;
const git=(...a)=>execFileSync('git',a,{encoding:'utf8'}).trim();
const read=()=>JSON.parse(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
const mergedFile=path.join(root,'merged');
const merged=()=>fs.existsSync(mergedFile)?fs.readFileSync(mergedFile,'utf8'):'';
const event=(s)=>fs.appendFileSync(path.join(root,'events'),s+'\n');
if(name==='gh') {
 if(args[0]==='pr' && args[1]==='create') { event('worker-pr'); console.log('https://github.com/fixture/project/pull/17'); }
 else if(args[0]==='pr' && args[1]==='view') console.log(JSON.stringify(read()));
 else if(args[0]==='pr' && args[1]==='merge') {
  const pr=read();
  if(args[args.indexOf('--match-head-commit')+1]!==pr.headRefOid || !args.includes('--merge')) throw Error('unpinned merge');
  if(merged()) throw Error('merge repeated');
  const wd=fs.readFileSync(path.join(root,'workdir'),'utf8');
  const head=pr.headRefOid, tree=git('-C',wd,'rev-parse',head+'^{tree}');
  const base=git('-C',wd,'rev-parse','dev');
  const sha=git('-C',wd,'-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit-tree',tree,'-p',base,'-p',head,'-m','Merge fixture work');
  fs.writeFileSync(mergedFile,sha);event('merge');
 } else if(args[0]==='api' && args.includes('--slurp')) console.log('[[]]');
 else if(args[0]==='api') {
  const pr=read(), sha=merged();
  const endpoint=args.find(a=>a.startsWith('repos/')) ?? 'graphql';
  let body;
  if(endpoint==='graphql') body={data:{repository:{pullRequest:{headRefOid:pr.headRefOid,reviewThreads:{nodes:[],pageInfo:{hasNextPage:false}}}}}};
  else if(endpoint.endsWith('/pulls/17')) body={number:17,state:sha?'closed':'open',merged:!!sha,head:{sha:pr.headRefOid},base:{ref:'dev',repo:{full_name:'fixture/project'}},merge_commit_sha:sha};
  else if(endpoint.includes('/check-runs?')) body={total_count:1,check_runs:[{name:'foundation',status:'completed',conclusion:'success'}]};
  else if(endpoint.includes('/status?')) body={sha:endpoint.split('/')[4],total_count:0,statuses:[]};
  else if(endpoint.includes('/git/ref/')) body={ref:'refs/heads/dev',object:{type:'commit',sha}};
  else if(endpoint.includes('/commits/')) body={sha};
  else throw Error('unhandled endpoint '+endpoint);
  process.stdout.write('HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'+JSON.stringify(body));
 } else throw Error('unexpected gh operation');
} else {
 const revision=process.env.LINA_GOAL_REVISION, target=process.env.LINA_GOAL_TARGET;
 if(process.env.LINA_RESPONSES_API_KEY) throw Error('Core credential leaked');
 let passed=false, evidence='';
 if(name==='qa' || name==='acceptance') {
  passed=git('rev-parse','HEAD')===revision && revision===merged() && fs.readFileSync('result.txt','utf8')==='worker produced this result\n';
  evidence='Read result.txt from the merged commit';
 } else if(name==='deploy') {
  if(git('rev-parse','HEAD')!==revision || revision!==merged()) throw Error('wrong deploy checkout');
  const response=await fetch(process.env.LINA_GOAL_FIXTURE_URL,{method:'POST',body:JSON.stringify({revision,body:fs.readFileSync('result.txt','utf8')})});
  passed=response.ok;evidence='Service accepted the merged artifact';
 } else if(name==='probe') {
  const response=await fetch(process.env.LINA_GOAL_FIXTURE_URL);
  const result=await response.json();
  passed=response.ok && result.revision===revision && result.body==='worker produced this result\n';
  evidence='Read the running service revision and response';
 } else throw Error('unexpected check '+name);
 event(name);
 console.log(JSON.stringify({revision,target,passed,evidence}));
 if(!passed) process.exitCode=1;
}
