#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Local vendor/service fixtures; the installed Core owns every driver action.
import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
const args=process.argv.slice(2);
const name=path.basename(process.argv[1]);
const root=process.env.LINA_WORKER_FIXTURE;
const git=(...a)=>execFileSync('git',a,{encoding:'utf8'}).trim();
const read=()=>JSON.parse(fs.readFileSync(path.join(root,'pr.json'),'utf8'));
const mergedFile=path.join(root,'merged');
const merged=()=>fs.existsSync(mergedFile)?fs.readFileSync(mergedFile,'utf8'):'';
const event=(s)=>fs.appendFileSync(path.join(root,'events'),s+'\n');
const merge=(source)=>{
 const pr=read();
  const wd=fs.readFileSync(path.join(root,'workdir'),'utf8');
  const head=pr.headRefOid, tree=git('-C',wd,'rev-parse',head+'^{tree}');
  const base=JSON.parse(fs.readFileSync(path.join(root,'allocation.json'),'utf8')).Base;
  const sha=git('-C',wd,'-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit-tree',tree,'-p',base,'-p',head,'-m','Merge fixture work');
  // Publish the fixture merge object as a real remote would. The source clone's
  // protected branch stays unchanged; QA later checks out this exact object.
  git('-C',path.join(root,'repo'),'fetch','--no-tags',wd,sha);
  fs.writeFileSync(mergedFile,sha);event(source);
};
if(name==='host-report') {
 console.log(JSON.stringify({cpu_cores:1,load_cpu:0,available_memory_mb:1024,available_disk_mb:1024}));
} else if(name==='gh') {
 if(args[0]==='pr' && args[1]==='create') { event('worker-pr'); console.log('https://github.com/fixture/project/pull/17'); }
 else if(args[0]==='pr' && args[1]==='view') {
  if(process.env.LINA_GOAL_MODE?.startsWith('completion-') && !fs.existsSync(path.join(root,'owner-path-proof'))) {
   // Simulate a completed CI owner-path check against the actual worker head.
   // Reading this observation later does not rerun the check.
   const pr=read(), wd=fs.readFileSync(path.join(root,'workdir'),'utf8');
   const passed=git('-C',wd,'show',pr.headRefOid+':result.txt')==='worker produced this result';
   pr.statusCheckRollup.push({__typename:'CheckRun',name:'owner-path-run',status:'COMPLETED',conclusion:passed?'SUCCESS':'FAILURE'});
   fs.writeFileSync(path.join(root,'pr.json'),JSON.stringify(pr));
   fs.writeFileSync(path.join(root,'owner-path-proof'),pr.headRefOid);
   event('owner-path-check');
  }
  if(process.env.LINA_GOAL_MODE==='external-merge') {
   const countFile=path.join(root,'pr-reads');
   const count=(fs.existsSync(countFile)?Number(fs.readFileSync(countFile,'utf8')):0)+1;
   fs.writeFileSync(countFile,String(count));
   // Worker delivery and goal inspection each read twice. Merge between
   // inspection and execution, before Core has created a merge binding.
   if(count===5) merge('external-merge');
  }
  console.log(JSON.stringify({...read(),state:merged()?'MERGED':'OPEN'}));
 }
 else if(args[0]==='pr' && args[1]==='merge') {
  const pr=read();
  if(args[args.indexOf('--match-head-commit')+1]!==pr.headRefOid || !args.includes('--merge')) throw Error('unpinned merge');
  if(merged()) throw Error('merge repeated');
  merge('merge');
 } else if(args[0]==='api' && args.includes('--slurp')) console.log('[[]]');
 else if(args[0]==='api') {
  const pr=read(), sha=merged();
  const endpoint=args.find(a=>a.startsWith('repos/')) ?? 'graphql';
  let body;
  if(endpoint==='graphql') body={data:{repository:{pullRequest:{headRefOid:pr.headRefOid,reviewThreads:{nodes:[],pageInfo:{hasNextPage:false}}}}}};
  else if(endpoint.endsWith('/pulls/17')) body={number:17,state:sha?'closed':'open',merged:!!sha,head:{sha:pr.headRefOid},base:{ref:'dev',repo:{full_name:'fixture/project'}},merge_commit_sha:sha};
  else if(endpoint.includes('/check-runs?')) {
   const checks=pr.statusCheckRollup.filter(c=>c.__typename==='CheckRun').map(c=>({name:c.name,status:c.status.toLowerCase(),conclusion:c.conclusion.toLowerCase()}));
   body={total_count:checks.length,check_runs:checks};
  }
  else if(endpoint.includes('/status?')) body={sha:endpoint.split('/')[4],total_count:0,statuses:[]};
  else if(endpoint.includes('/git/ref/')) body={ref:'refs/heads/dev',object:{type:'commit',sha}};
  else if(endpoint.includes('/commits/')) body={sha};
  else throw Error('unhandled endpoint '+endpoint);
  process.stdout.write('HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'+JSON.stringify(body));
 } else throw Error('unexpected gh operation');
} else {
 const revision=process.env.LINA_GOAL_REVISION, target=process.env.LINA_GOAL_TARGET;
 if(process.env.LINA_RESPONSES_API_KEY) throw Error('Core credential leaked');
 let passed=false, evidence='', collected;
 const calendar=path.join(root,'calendar.json');
 if(name==='calendar') {
  // A plugin's success claim is intentionally independent of the real fields.
  // Core must compare readback against the criterion's requested record.
  collected={record:JSON.parse(fs.readFileSync(calendar,'utf8'))};
  passed=true;evidence='Read calendar event';
 } else if(name==='citations' || name==='rubric') {
  const wd=fs.readFileSync(path.join(root,'workdir'),'utf8');
  const content=fs.readFileSync(path.join(wd,'result.txt'),'utf8');
  const revision=createHash('sha256').update(content).digest('hex');
  const reads=[{target:'document',revision,content}];
  passed=content==='worker produced this result\n';
  collected=name==='citations'?{reads,output:'Cited passage: '+content}:{reads,rubric:JSON.parse(process.env.LINA_GOAL_CHECK).rubric,judgment:'The document contains the requested result'};
  evidence='Read the document';
  if(name==='rubric' && process.env.LINA_GOAL_MODE==='evidence-changed') {
   // Calendar was checked earlier; mutate it before Core's closing readback.
   fs.writeFileSync(calendar,JSON.stringify({id:'event',time:'10:00',place:'A'}));
  }
 } else if(name==='qa' || name==='acceptance') {
  passed=git('rev-parse','HEAD')===revision && revision===merged() && fs.readFileSync('result.txt','utf8')==='worker produced this result\n';
  evidence='Read result.txt from the merged commit';
 } else if(name==='deploy') {
  if(git('rev-parse','HEAD')!==revision || revision!==merged()) throw Error('wrong deploy checkout');
  event('deploy');
  const response=await fetch(process.env.LINA_GOAL_FIXTURE_URL,{method:'POST',body:JSON.stringify({revision,body:fs.readFileSync('result.txt','utf8')})});
  if(process.env.LINA_GOAL_MODE?.startsWith('restart-')){setInterval(()=>{},1000);await new Promise(()=>{});}
  passed=response.ok && process.env.LINA_GOAL_MODE!=='failed-deploy';evidence='Service accepted the merged artifact';
  if(process.env.LINA_GOAL_MODE?.startsWith('evidence-')) {
   // Stub plugin effect: its receipt says only that the request was accepted.
   fs.writeFileSync(path.join(root,'effect-receipt.json'),JSON.stringify({accepted:true}));
   fs.writeFileSync(calendar,JSON.stringify({id:'event',time:process.env.LINA_GOAL_MODE==='evidence-mismatch'?'10:00':'09:00',place:'A'}));
  }
 } else if(name==='probe') {
  const response=await fetch(process.env.LINA_GOAL_FIXTURE_URL);
  const result=await response.json();
  passed=response.ok && result.revision===revision && result.body==='worker produced this result\n';
  evidence='Read the running service revision and response';
 } else throw Error('unexpected check '+name);
 if(name!=='deploy') event(name);
 console.log(JSON.stringify({revision,target,passed,evidence,collected}));
 if(!passed) process.exitCode=1;
}
