// SPDX-License-Identifier: Apache-2.0
// Classifies a change into the checks it needs. The path map below is the executable registry:
// a path that no entry covers is unmapped, and the gate refuses to pass until it is registered.
import { existsSync } from 'node:fs';
import { join, relative } from 'node:path';
import { git, gitOut, resolveCommit } from '../git.mjs';
import { createGoContext, execute } from './check-components.mjs';

const documents = new Set([
  'README.md', 'MANIFESTO.md', 'LICENSE', 'THIRD-PARTY-NOTICES.md', 'SECURITY.md', 'AGENTS.md', 'CONTRIBUTING.md', 'NOTICE', 'docs/qa.md',
]);
const configuration = new Set(['.editorconfig', '.gitattributes', '.gitignore', '.node-version', '.devflow']);
const modules = new Set(['go.mod', 'go.sum', 'go.work', 'go.work.sum', 'kit/go.mod', 'kit/go.sum']);

function registryFor(cwd) {
  // Older trees and minimal verification fixtures may not have these packages yet.
  const architecture = existsSync(join(cwd, 'internal/archtest')) ? ['./internal/archtest/...'] : [];
  const buildinfo = existsSync(join(cwd, 'internal/buildinfo')) ? ['./internal/buildinfo/...'] : [];
  const manifest = existsSync(join(cwd, 'internal/core/manifest')) ? ['./internal/core/manifest/...'] : [];
  // Keep registrations sorted, one per line, so independent additions can merge.
  const coreDirectories = [
    'internal/core/accounts/',
    'internal/core/agents/',
    'internal/core/assemble/',
    'internal/core/assets/',
    'internal/core/authority/',
    'internal/core/boot/',
    'internal/core/budget/',
    'internal/core/canon/',
    'internal/core/channels/',
    'internal/core/clientsrv/',
    'internal/core/ctlfiles/',
    'internal/core/eventstore/',
    'internal/core/exec/',
    'internal/core/gateway/',
    'internal/core/guard/',
    'internal/core/importer/',
    'internal/core/inbox/',
    'internal/core/install/',
    'internal/core/jobs/',
    'internal/core/lane/',
    'internal/core/ledger/',
    'internal/core/lifecycle/',
    'internal/core/memory/',
    'internal/core/modelpath/',
    'internal/core/reply/',
    'internal/core/results/',
    'internal/core/runs/',
    'internal/core/secrets/',
    'internal/core/skills/',
    'internal/core/turn/',
    'internal/core/waiting/',
  ];
  const skills = existsSync(join(cwd, 'skills')) ? ['./skills/...'] : [];
