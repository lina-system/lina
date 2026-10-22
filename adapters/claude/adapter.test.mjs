// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PassThrough } from 'node:stream';
import { serve } from './adapter.ts';

const start = { type: 'start', id: 'turn', system: 'Synthetic persona', model: 'fixture', items: [{ role: 'user', content: 'use a tool' }], tools: [{ name: 'lookup', description: 'lookup', parameters: { type: 'object', properties: {} } }] };
const init = { type: 'system', subtype: 'init', tools: ['mcp__lina__lookup'], mcp_servers: [{ name: 'lina', status: 'connected', source: 'sdk' }], plugins: [] };
function harness(t, runtime) {
  const input = new PassThrough(), output = new PassThrough();
  let buffer = '';
  const messages = [], pending = [];
  output.on('data', chunk => {
    buffer += chunk;
    for (;;) {
      const end = buffer.indexOf('\n');
      if (end < 0) break;
      const message = JSON.parse(buffer.slice(0, end));
      buffer = buffer.slice(end + 1);
      if (pending.length) pending.shift()(message); else messages.push(message);
    }
  });
  const finished = serve(input, output, runtime);
  t.after(async () => { input.end(); await finished; });
  return {
    send: value => input.write(JSON.stringify(value) + '\n'), input, finished,
    next: () => messages.length ? Promise.resolve(messages.shift()) : new Promise(resolve => pending.push(resolve)),
  };
}

test('stdio bridges init, text, tools/results, usage, limits and terminal', { timeout: 5000 }, async t => {
  const h = harness(t, async function* (request, call) {
    assert.deepEqual(request.items, start.items);
    assert.equal(request.system, start.system);
    yield init;
    yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'working' } } };
    const result = await call('lookup', { key: 'fixture' });
    assert.deepEqual(result, { content: [{ type: 'text', text: 'found' }] });
    yield { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'found' }] } };
    yield { type: 'rate_limit_event', rate_limit_info: { status: 'allowed', rateLimitType: 'five_hour', utilization: 0.2 } };
    yield { type: 'result', subtype: 'success', session_id: 'session', total_cost_usd: 0.01, usage: { input_tokens: 5, output_tokens: 2 } };
  });
  h.send(start);
  assert.equal((await h.next()).type, 'init');
  assert.equal((await h.next()).text, 'working');
  const call = await h.next();
  assert.equal(call.type, 'tool_call');
  assert.equal(call.name, 'lookup');
  h.send({ type: 'tool_result', id: start.id, call_id: call.call_id, result: { content: [{ type: 'text', text: 'found' }] } });
  assert.equal((await h.next()).type, 'item_done');
  assert.equal((await h.next()).type, 'limit');
  assert.equal((await h.next()).usage.input, 5);
  assert.deepEqual(await h.next(), { type: 'terminal', id: 'turn', reason: 'completed', session_id: 'session', cost_estimate: { currency: 'USD', micros: 10000 } });
});

for (const loaded of [{ ...init, tools: ['Bash'] }, { ...init, mcp_servers: [{ name: 'foreign' }] }, { ...init, plugins: [{ name: 'foreign' }] }]) {
  test('foreign runtime capability is refused before output or tools', { timeout: 5000 }, async t => {
    const h = harness(t, async function* () { yield loaded; yield { type: 'assistant', message: { content: 'must not escape' } }; });
    h.send(start);
    assert.equal((await h.next()).code, 'unexpected_runtime');
    assert.equal((await h.next()).reason, 'failed');
  });
}

test('cancel interrupts a pending tool; ping remains responsive', { timeout: 5000 }, async t => {
  let aborted = false;
  const h = harness(t, async function* (_, call, signal) {
    signal.addEventListener('abort', () => { aborted = true; });
    yield init;
    await call('lookup', {});
  });
  h.send(start);
  await h.next(); await h.next();
  h.send({ type: 'ping', id: 'probe' });
  assert.deepEqual(await h.next(), { type: 'pong', id: 'probe' });
  h.send({ type: 'cancel', id: 'turn' });
  assert.equal((await h.next()).reason, 'cancelled');
  assert.equal(aborted, true);
});

test('truncated runtime never reports completion or leaks errors', { timeout: 5000 }, async t => {
  const h = harness(t, async function* () { yield init; throw new Error('synthetic-secret'); });
  h.send(start); await h.next();
  const error = await h.next();
  assert.equal(error.code, 'runtime_failed');
  assert.ok(!JSON.stringify(error).includes('synthetic-secret'));
  assert.equal((await h.next()).reason, 'failed');
});

test('EOF cancels and joins active runtime', { timeout: 5000 }, async t => {
  let stopped = false;
  const h = harness(t, async function* (_, call) { try { yield init; await call('lookup', {}); } finally { stopped = true; } });
  h.send(start); await h.next(); await h.next();
  h.input.end(); await h.finished;
  assert.equal(stopped, true);
});

test('concurrent turns stay correlated and reject duplicates or capacity overflow', { timeout: 5000 }, async t => {
  const h = harness(t, async function* (_, call) { yield init; await call('lookup', {}); });
  h.send(start); await h.next(); const first = await h.next();
  h.send({ ...start, id: 'second' }); await h.next(); const second = await h.next();
  assert.equal(first.id, 'turn'); assert.equal(second.id, 'second');
  h.send(start); assert.equal((await h.next()).code, 'turn_unavailable');
  h.send({ ...start, id: 'third' }); assert.equal((await h.next()).code, 'turn_unavailable');
  h.send({ type: 'tool_result', id: 'second', call_id: first.call_id, result: { content: [] } });
  assert.equal((await h.next()).code, 'invalid_tool_result');
  h.send({ type: 'cancel', id: 'turn' }); assert.equal((await h.next()).id, 'turn');
  h.send({ type: 'cancel', id: 'second' }); assert.equal((await h.next()).id, 'second');
});

test('missing init and early runtime EOF fail closed', { timeout: 5000 }, async t => {
  const h = harness(t, async function* () { yield { type: 'result', subtype: 'success' }; });
  h.send(start);
  assert.equal((await h.next()).code, 'missing_init');
  assert.equal((await h.next()).reason, 'failed');
});

test('malformed JSON is isolated and truncated input terminates cleanly', { timeout: 5000 }, async t => {
  const h = harness(t, async function* () {});
  h.input.write('invalid\n'); assert.equal((await h.next()).code, 'invalid_message');
  h.send({ type: 'ping', id: 'still-live' }); assert.equal((await h.next()).type, 'pong');
  h.input.end('{'); assert.equal((await h.next()).code, 'truncated_frame');
  await h.finished;
});
