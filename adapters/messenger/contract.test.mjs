// SPDX-License-Identifier: Apache-2.0
// Contract tests for the messenger add-on. Recorded synthetic responses stand in
// for a platform; the pinned SDK's surface is enumerated from the package, which
// is never installed, run or signed in here.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PassThrough } from 'node:stream';
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { serve, describe as describeError } from './src/serve.ts';
import { parseRequest, OPS } from './src/protocol.ts';
import { PLATFORMS } from './src/platforms.ts';
import { READ_METHODS, FORBIDDEN_METHODS } from './src/allowlist.ts';
import { fixtureDriver } from './fixture-driver.ts';

const sourceDir = new URL('./src/', import.meta.url);
const sources = () => readdirSync(sourceDir)
  .filter(name => name.endsWith('.ts'))
  .map(name => [name, readFileSync(new URL(name, sourceDir), 'utf8')]);

function harness(t, driver = fixtureDriver()) {
  const input = new PassThrough(), output = new PassThrough();
  let buffer = '', lines = [];
  output.on('data', chunk => {
    buffer += chunk;
    for (;;) {
      const end = buffer.indexOf('\n');
      if (end < 0) break;
      lines.push(JSON.parse(buffer.slice(0, end)));
      buffer = buffer.slice(end + 1);
    }
  });
  const finished = serve(input, output, { open: async () => driver }, { platform: 'fixture', account: 'account-1' });
  t.after(async () => {
    input.end();
    await finished;
  });
  return {
    driver,
    async send(value) {
      lines = [];
      input.write(`${typeof value === 'string' ? value : JSON.stringify(value)}\n`);
      for (let i = 0; i < 400 && lines.length === 0; i++) await new Promise(resolve => setTimeout(resolve, 5));
      return lines;
    },
    lines: () => lines,
  };
}

test('every exposed operation answers with a recorded synthetic response', async t => {
  const h = harness(t);
  const expected = {
    hello: { version: 1, platform: 'fixture', account: 'account-1' },
    signIn: { state: 'signed_in' },
    beginSignIn: { state: 'signed_in' },
    listChats: { chats: [{ id: 'dm-1', name: 'Direct', kind: 'direct' }] },
    page: {
      messages: [{ id: 'm-1', author: { id: 'u1', name: 'Someone' }, route: 'external_participant', at: '2026-10-24T09:00:00.000Z', text: 'hello' }],
      deletions: [], next: 'c1', hasMore: true,
    },
    members: { members: [{ id: 'u1', name: 'Someone' }] },
    whoami: { author: { id: 'u0', name: 'Me' } },
    answer: { state: 'signed_in', prompt: undefined, show: undefined },
    live: { live: true },
    stop: { stopped: true },
  };
  for (const [op, result] of Object.entries(expected)) {
    const args = op === 'page' ? { chat: 'dm-1', cursor: '' } : op === 'members' ? { chat: 'dm-1' } : op === 'answer' ? { prompt: 'email', value: 'a@example.com' } : {};
    const [frame] = await h.send({ id: op, op, ...args });
    assert.equal(frame.ok, true, op);
    // An absent field is serialized away, so compare the fields the frame has.
    for (const [key, value] of Object.entries(result)) {
      if (value !== undefined) assert.deepEqual(frame.result[key], value, `${op}.${key}`);
    }
  }
  assert.deepEqual(h.driver.calls, ['signIn', 'beginSignIn', 'listChats', 'page', 'members', 'whoami', 'answer', 'live', 'stop']);
});

test('the operation table offers no write, extraction or read-state call', async t => {
  const h = harness(t);
  for (const op of ['sendMessage', 'markRead', 'getReadWatermarks', 'extractToken', 'loginWithRemoteAuth', 'connectForPairing', 'login', 'request', 'uploadFile']) {
    const [frame] = await h.send({ id: 'x', op });
    assert.deepEqual(frame, { id: null, ok: false, error: 'unknown_operation' }, op);
  }
  assert.deepEqual(h.driver.calls, []);
  for (const op of OPS) assert.ok(!/send|mark|extract|takeover|force|upload|delete|leave|ack|typing|edit|login/i.test(op), op);
});

test('a request with an unexpected field or a bad identifier is refused', () => {
  for (const line of [
    '{"id":"a","op":"page","chat":"dm-1","cursor":"","extra":1}',
    '{"id":"","op":"hello"}',
    '{"id":"a b","op":"hello"}',
    '{"id":"a","op":"page","chat":"dm 1","cursor":""}',
    '{"id":"a","op":"answer","prompt":"email","value":1}',
    '[]',
    'not json',
    '{"id":"a","op":"page","chat":"dm-1"}',
  ]) {
    assert.throws(() => parseRequest(line), line);
  }
  assert.equal(parseRequest('{"id":"a","op":"hello"}').op, 'hello');
});

test('a line past the limit is refused and the loop keeps serving', async t => {
  const h = harness(t);
  const [refusal] = await h.send(`{"id":"a","op":"hello","pad":"${'x'.repeat(8 * 1024 * 1024)}"}`);
  assert.equal(refusal.ok, false);
  const [frame] = await h.send({ id: 'b', op: 'hello' });
  assert.equal(frame.ok, true);
});

test('a response shape the add-on does not know is refused, not passed on', async t => {
  const h = harness(t, fixtureDriver({ page: { messages: [{ id: 'm-1' }], deletions: [], next: '', hasMore: false } }));
  const [frame] = await h.send({ id: 'p', op: 'page', chat: 'dm-1', cursor: '' });
  assert.deepEqual(frame, { id: 'p', ok: false, error: 'upstream_changed' });
});

test('a page coverage the add-on does not know is refused', async t => {
  const h = harness(t, fixtureDriver({ page: { messages: [], deletions: [], next: '', hasMore: false, coverage: { historyDepth: 7 } } }));
  const [frame] = await h.send({ id: 'p', op: 'page', chat: 'dm-1', cursor: '' });
  assert.deepEqual(frame, { id: 'p', ok: false, error: 'upstream_changed' });
});

test('a decryption failure that carries text is refused', async t => {
  const message = {
    id: 'm-1', author: { id: 'u-1', name: 'A' }, route: 'external_participant',
    at: '2026-10-01T00:00:00Z', text: 'visible', decryptionFailed: true,
  };
  const h = harness(t, fixtureDriver({ page: { messages: [message], deletions: [], next: '', hasMore: false } }));
  const [frame] = await h.send({ id: 'p', op: 'page', chat: 'dm-1', cursor: '' });
  assert.deepEqual(frame, { id: 'p', ok: false, error: 'upstream_changed' });
});

test('a driver failure names the platform code, never the SDK message', async t => {
  const failure = Object.assign(new Error('token abc123 at /synthetic/home'), { code: 'auth_expired' });
  const h = harness(t, Object.assign(fixtureDriver(), { signInState: async () => { throw failure; } }));
  const [frame] = await h.send({ id: 's', op: 'signIn' });
  assert.deepEqual(frame, { id: 's', ok: false, error: 'failed:auth_expired' });
  assert.equal(describeError(failure), 'failed:auth_expired');
});

test('live events reach the caller until the subscription stops', async t => {
  const h = harness(t);
  await h.send({ id: 'l', op: 'live' });
  await h.send({ id: 'ping', op: 'whoami' });
  h.driver.emit({ event: 'message', message: { id: 'm-9', author: { id: 'u1', name: 'Someone' }, route: 'external_participant', at: '2026-10-24T09:00:00.000Z', text: 'live' } });
  for (let i = 0; i < 400 && !h.lines().some(line => line.event === 'message'); i++) await new Promise(resolve => setTimeout(resolve, 5));
  assert.deepEqual(h.lines().find(line => line.event === 'message').message.id, 'm-9');
  await h.send({ id: 's', op: 'stop' });
  assert.equal(h.driver.delivered.length, 1);
});

test('no add-on source names an SDK method outside the read allowlist', () => {
  assert.deepEqual(Object.keys(PLATFORMS).sort(), ['discord', 'kakaotalk', 'line', 'whatsapp']);
  const allowed = new Set(Object.values(READ_METHODS).flat());
  for (const [name, source] of sources()) {
    for (const method of FORBIDDEN_METHODS) {
      assert.ok(!new RegExp(`\\b${method}\\s*\\(`).test(source), `${name} calls ${method}`);
    }
    for (const match of source.matchAll(/\b(?:client|sdk|credentials|manager)\.([A-Za-z_$][\w$]*)\s*\(/g)) {
      assert.ok(allowed.has(match[1]), `${name} calls ${match[1]}`);
    }
  }
});

test('the manifest pins agent-messenger 2.39.1 without optional native packages', () => {
  const manifest = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8'));
  assert.equal(manifest.dependencies['agent-messenger'], '2.39.1');
  assert.equal(manifest.optionalDependencies, undefined);
  assert.deepEqual(Object.keys(manifest.scripts ?? {}).filter(name => /^(pre|post|install)/.test(name)), []);
  const lock = JSON.parse(readFileSync(new URL('./package-lock.json', import.meta.url), 'utf8'));
  assert.equal(lock.packages[''].dependencies['agent-messenger'], '2.39.1');
});

// Inspect the exact installed pin without importing or executing SDK code.
test('every SDK write and extraction entry point is denied by the dispatcher', async t => {
  const root = new URL('./node_modules/agent-messenger/src/platforms/', import.meta.url);
  if (!existsSync(root)) { t.skip('installed SDK inventory runs in the dependency check'); return; }
  const dangerous = /^(send|mark|ack|add|remove|edit|delete|create|archive|upload|setUser|setNote|extract|force|leave|join|fetchRead|getRead|getUnread|request|gateway)/;
  const methods = new Set();
  for (const platform of Object.keys(PLATFORMS)) {
    const dir = new URL(platform + '/', root);
    for (const file of readdirSync(dir, { recursive: true })) {
      if (!file.endsWith('.ts') || file.includes('.test.')) continue;
      const source = readFileSync(new URL(file, dir), 'utf8');
      for (const match of source.matchAll(/^  (?:(?:public|static|async) )*([A-Za-z_$][\w$]*)\s*\(/gm)) {
        if (dangerous.test(match[1])) methods.add(match[1]);
      }
    }
  }
  assert.ok(methods.size > 30, 'enumerated pinned SDK methods');
  const h = harness(t);
  for (const method of methods) {
    assert.ok(FORBIDDEN_METHODS.includes(method), `unclassified upstream method: ${method}`);
    const [reply] = await h.send({ id: 'blocked', op: method });
    assert.equal(reply.error, 'unknown_operation', method);
  }
  assert.deepEqual(h.driver.calls, []);
});
