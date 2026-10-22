// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// Exercise selection, not just the path map: subprocess acceptance tests must
// be part of the packages foundation actually executes.
func TestFoundationIncludesE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", `
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { git } from './scripts/git.mjs';
import { componentRegistry, kind, scopeFor } from './scripts/verify/scope.mjs';
const scope = scopeFor({ head: 'HEAD', full: true });
assert.equal(kind('e2e/m1/scenario_test.go'), 'components');
assert.equal(kind('e2e/fixtures/scenario.json'), 'components');
assert.equal(kind('e2e/README.md'), 'components');
assert.equal(kind('internal/fakecore/core.go'), 'components');
const selected = componentRegistry.filter(c => scope.selectedComponents.includes(c.name));
assert.ok(selected.some(c => c.packages.includes('./e2e/...')), 'foundation must execute e2e');
assert.ok(selected.some(c => c.packages.includes('./internal/fakecore/...')), 'foundation must execute fakecore');

// Core/TUI are subprocess dependencies, invisible to Go test imports. Exercise
// real component-only diffs as well as full selection in a disposable repo.
const cwd = mkdtempSync(join(tmpdir(), 'lina-e2e-scope-'));
try {
  const env = {
    ...Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('GIT_'))),
    GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_AUTHOR_NAME: 'Fixture', GIT_AUTHOR_EMAIL: 'fixture@example.invalid',
    GIT_COMMITTER_NAME: 'Fixture', GIT_COMMITTER_EMAIL: 'fixture@example.invalid',
  };
  const runGit = (...args) => {
    const result = git(cwd, args, { env });
    assert.equal(result.status, 0, result.stderr);
    return result.stdout.trim();
  };
  const files = {
    'go.mod': 'module example.invalid/lina\ngo 1.27\ntoolchain go1.27.3\n',
    'kit/go.mod': 'module example.invalid/lina/kit\ngo 1.27\ntoolchain go1.27.3\n',
    'go.work': 'go 1.27\ntoolchain go1.27.3\nuse (\n.\n./kit\n)\n',
    'doc.go': 'package lina\n',
    'kit/doc.go': 'package kit\n',
    'cmd/lina/main.go': 'package main\nimport _ "example.invalid/lina/kit"\nfunc main() {}\n',
    'internal/core/clientsrv/server.go': 'package clientsrv\n',
    'internal/tui/ui/ui.go': 'package ui\n',
    'internal/fakecore/core.go': 'package fakecore\n',
    'e2e/m1/scenario_test.go': 'package e2e_test\n',
    'README.md': '# Fixture\n',
  };
  for (const [path, contents] of Object.entries(files)) {
    mkdirSync(dirname(join(cwd, path)), { recursive: true });
    writeFileSync(join(cwd, path), contents);
  }
  runGit('init', '--initial-branch=dev');
  runGit('add', '.');
  runGit('commit', '-m', 'fixture');
  const base = runGit('rev-parse', 'HEAD');
  for (const path of ['e2e/m1/scenario_test.go', 'cmd/lina/main.go',
      'internal/core/clientsrv/server.go', 'internal/tui/ui/ui.go', 'internal/fakecore/core.go', 'kit/doc.go']) {
    writeFileSync(join(cwd, path), files[path] + '// changed\n');
    const changed = scopeFor({ cwd, base, head: 'HEAD' });
    assert.ok(changed.selectedComponents.includes('lina'), path + ' must run the e2e component');
    assert.deepEqual(changed.unmapped, []);
    writeFileSync(join(cwd, path), files[path]);
  }
  writeFileSync(join(cwd, 'README.md'), '# Updated fixture\n');
  assert.equal(scopeFor({ cwd, base, head: 'HEAD' }).components, false);
} finally {
  rmSync(cwd, { recursive: true, force: true });
}
`)
	command.Dir = "../.."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("foundation selection: %v\n%s", err, output)
	}
}
