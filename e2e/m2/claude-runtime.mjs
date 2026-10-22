// SPDX-License-Identifier: Apache-2.0
// Test-only runtime. It drives the production adapter and Core tool bridge.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { serve } from '../../adapters/claude/adapter.ts';
const scenario = JSON.parse(await readFile(new URL('./claude-adapter.json', import.meta.url)));
await serve(process.stdin, process.stdout, async function* (start, call) {
  assert.ok(start.system.length > 0);
  assert.ok(JSON.stringify(start.items).includes(scenario.input));
  assert.ok(start.tools.some(t => t.name === scenario.tool));
  yield { type: 'system', subtype: 'init', tools: start.tools.map(t => `mcp__lina__${t.name}`), mcp_servers: [{ name: 'lina', source: 'sdk', status: 'connected' }], plugins: [] };
  await call(scenario.tool, {});
  const result = await call(scenario.tool, {});
  const receipt = JSON.parse(result.content[0].text);
  assert.equal(receipt.code, 'ok');
  assert.ok(JSON.stringify(receipt.output).includes('description'));
  yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: scenario.reply } } };
  yield { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: scenario.reply }] } };
  yield { type: 'result', subtype: 'success', session_id: 'fixture-session', usage: { input_tokens: 5, output_tokens: 3 }, total_cost_usd: 0.001 };
});
