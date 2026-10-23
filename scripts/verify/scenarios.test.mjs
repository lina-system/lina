// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import { sandbox } from '../test-fixture.mjs';
import { scanScenarios } from './scenarios.mjs';

test('Go syntax identifies runnable declarations and follows shared helpers and binaries', t => {
  const sb = sandbox(t);
  const toolchain = 'go 1.27\ntoolchain go1.27.3\n';
  sb.write(sb.dir, 'go.mod', `module example.invalid/lina\n${toolchain}`);
  sb.write(sb.dir, 'kit/go.mod', `module example.invalid/lina/kit\n${toolchain}`);
  sb.write(sb.dir, 'go.work', `${toolchain}use (\n.\n./kit\n)\n`);
  sb.write(sb.dir, 'e2e/m1/one_test.go', `package fixture_test
import ("testing"; "example.invalid/direct")
// func TestComment(t *testing.T) {}
var source = "func TestString(t *testing.T) {}"
func TestOne(t *testing.T) { launch(t) }
func Testlower(t *testing.T) {}
func FuzzSeed(f *testing.F) {}
func ExampleVisible() {
 // Output: visible
}
func ExampleHidden() {}
type receiver struct{}
func (receiver) TestMethod(t *testing.T) {}
`);
  sb.write(sb.dir, 'e2e/m1/harness_test.go', `package fixture_test
import ("testing"; "example.invalid/helper")
func launch(t *testing.T) { buildCommand(t, "worker"); fixture("../m2/input.json") }
func buildCommand(t *testing.T, name string) {}
func fixture(name string) {}
`);
  sb.write(sb.dir, 'e2e/m2/input.json', '{}\n');
  sb.write(sb.dir, 'e2e/m1/helper.go', 'package fixture_test\nfunc TestNotInATestFile() {}\n');
  const result = scanScenarios({ cwd: sb.dir, packages: [
    { path: 'example.invalid/lina/e2e/m1', directory: 'e2e/m1', sourceFiles: ['e2e/m1/one_test.go', 'e2e/m1/harness_test.go', 'e2e/m1/helper.go'] },
    { path: 'example.invalid/lina/cmd/worker', directory: 'cmd/worker', sourceFiles: [] },
  ] });
  assert.equal(result.scenarios.length, 1);
  const scenario = result.scenarios[0];
  assert.deepEqual(scenario.tests, ['ExampleVisible', 'FuzzSeed', 'TestOne']);
  assert.ok(scenario.imports.includes('example.invalid/direct'));
  assert.ok(scenario.imports.includes('example.invalid/helper'));
  assert.deepEqual(scenario.binaries, ['example.invalid/lina/cmd/worker']);
  assert.ok(scenario.inputs.includes('e2e/m1/harness_test.go'));
  assert.ok(scenario.inputs.includes('e2e/m2/input.json'));
  assert.ok(result.sharedFiles.includes('e2e/m1/harness_test.go'));
  const harness = readFileSync(join(sb.dir, 'e2e/m1/harness_test.go'), 'utf8');
  sb.write(sb.dir, 'e2e/m1/harness_test.go', harness.replace('buildCommand(t, "worker")', 'name := "worker"; buildCommand(t, name)'));
  const dynamic = scanScenarios({ cwd: sb.dir, packages: [
    { path: 'example.invalid/lina/e2e/m1', directory: 'e2e/m1', sourceFiles: ['e2e/m1/one_test.go', 'e2e/m1/harness_test.go'] },
    { path: 'example.invalid/lina/cmd/worker', directory: 'cmd/worker', sourceFiles: [] },
  ] });
  assert.equal(dynamic.scenarios[0].unknownBinary, true, 'unresolved command arguments must prevent pruning');
});

test('a package containing only TestMain still receives an acceptance invocation', t => {
  const sb = sandbox(t);
  const toolchain = 'go 1.27\ntoolchain go1.27.3\n';
  sb.write(sb.dir, 'go.mod', 'module example.invalid/lina\n' + toolchain);
  sb.write(sb.dir, 'kit/go.mod', 'module example.invalid/lina/kit\n' + toolchain);
  sb.write(sb.dir, 'go.work', toolchain + 'use (\n.\n./kit\n)\n');
  sb.write(sb.dir, 'e2e/main_test.go', 'package fixture_test\nimport "testing"\nfunc TestMain(m *testing.M) { m.Run() }\n');
  const result = scanScenarios({ cwd: sb.dir, packages: [
    { path: 'example.invalid/lina/e2e', directory: 'e2e', sourceFiles: ['e2e/main_test.go'] },
  ] });
  assert.equal(result.scenarios.length, 1);
  assert.deepEqual(result.scenarios[0].tests, []);
  assert.deepEqual(result.scenarios[0].inputs, ['e2e/main_test.go']);
});
