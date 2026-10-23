#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Synthetic GitHub boundary for installed Core delivery scenarios.
import fs from 'node:fs';
import path from 'node:path';
const args = process.argv.slice(2);
const root = process.env.LINA_WORKER_FIXTURE;
const file = path.join(root, 'pr.json');
if (args[0] === 'pr' && args[1] === 'create') {
  const mapFile = path.join(root,'created-prs.json');
  const allocated = fs.existsSync(mapFile) ? JSON.parse(fs.readFileSync(mapFile,'utf8')) : {};
  const task = path.basename(process.cwd());
  allocated[task] ??= 17 + Object.keys(allocated).length;
  fs.writeFileSync(mapFile,JSON.stringify(allocated));
  fs.writeFileSync(path.join(root, 'created'), '1');
  console.log(`https://github.com/fixture/project/pull/${allocated[task]}`);
  process.exit(0);
}
const pr = JSON.parse(fs.readFileSync(file, 'utf8'));
const save = () => fs.writeFileSync(file, JSON.stringify(pr));
const respond = value => {
  if (args.includes('--include')) process.stdout.write('HTTP/2.0 200 OK\r\n\r\n');
  console.log(JSON.stringify(value));
};
if (args[0] === 'pr') {
  if (args[1] === 'merge') {
    if (args[args.indexOf('--match-head-commit') + 1] !== pr.headRefOid) process.exit(1);
    fs.appendFileSync(path.join(root, 'merges'), pr.headRefOid + '\n');
    pr.state = 'MERGED'; save();
  } else if (args[1] === 'close') {
    fs.appendFileSync(path.join(root, 'closes'), args[args.indexOf('--comment') + 1] + '\n');
    pr.state = 'CLOSED'; save();
  } else if (args[1] === 'view') respond(pr);
  else throw Error('unexpected PR operation');
} else if (args[0] === 'api') {
  const endpoint = (args.find(value => value.startsWith('repos/')) || '').split('?')[0];
  if (args.includes('graphql')) respond({data:{repository:{pullRequest:{headRefOid:pr.headRefOid,reviewThreads:{nodes:[],pageInfo:{hasNextPage:false}}}}}});
  else if (endpoint.includes('/issues/') && endpoint.endsWith('/comments')) respond(fs.existsSync(path.join(root,'closes')) ? [{body:fs.readFileSync(path.join(root,'closes'),'utf8').slice(0,-1)}] : []);
  else if (endpoint.endsWith('/comments')) respond([[]]);
  else if (endpoint.endsWith(`/pulls/${pr.number}`)) respond({number:pr.number,state:pr.state==='OPEN'?'open':'closed',merged:pr.state==='MERGED',head:{sha:pr.headRefOid},base:{ref:'dev',repo:{full_name:'fixture/project'}},merge_commit_sha:pr.headRefOid});
  else if (endpoint.endsWith('/check-runs')) respond({total_count:1,check_runs:[{name:'foundation',status:'completed',conclusion:'success'}]});
  else if (endpoint.endsWith('/status')) respond({sha:pr.headRefOid,total_count:0,statuses:[]});
  else if (endpoint.endsWith('/git/ref/heads/dev')) respond({ref:'refs/heads/dev',object:{type:'commit',sha:pr.headRefOid}});
  else if (endpoint.includes('/commits/')) respond({sha:pr.headRefOid});
  else throw Error('unexpected API operation');
} else throw Error('unexpected gh operation');
