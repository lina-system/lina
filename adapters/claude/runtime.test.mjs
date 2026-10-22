// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { existsSync, readdirSync } from 'node:fs';
import * as sdk from '@anthropic-ai/claude-agent-sdk';
import * as z from 'zod/v4';
import { sdkRuntime, runtimeEnv } from './runtime.ts';

test('real SDK tool registration and explicit query isolation without a model request', async () => {
  let options, closed = false;
  const runtime = sdkRuntime({ ...sdk, query: request => {
    options = request.options;
    assert.deepEqual(JSON.parse(request.prompt).conversation, [{ role: 'user', content: 'synthetic' }]);
    assert.deepEqual(readdirSync(options.cwd), []);
    return { async *[Symbol.asyncIterator]() { yield { type: 'fixture' }; }, close() { closed = true; } };
  } }, z, { HOME: '/synthetic', CLAUDE_CODE_OAUTH_TOKEN: 'synthetic-token', ANTHROPIC_API_KEY: 'must-not-inherit', NODE_OPTIONS: '--import forbidden' });
  const result = [];
  for await (const event of runtime({ system: 'synthetic-system', model: 'fixture-model', items: [{ role: 'user', content: 'synthetic' }], tools: [{ name: 'lookup', parameters: { type: 'object', properties: { key: { type: 'string' } }, required: ['key'], additionalProperties: false } }] }, async () => ({ content: [] }), new AbortController().signal)) result.push(event);
  assert.equal(result.length, 1);
  assert.equal(options.systemPrompt, 'synthetic-system');
  assert.deepEqual(options.tools, []);
  assert.deepEqual(options.settingSources, []);
  assert.equal(options.strictMcpConfig, true);
  assert.equal(options.persistSession, false);
  assert.deepEqual(options.allowedTools, ['mcp__lina__lookup']);
  assert.equal(options.mcpServers.lina.type, 'sdk');
  assert.equal(options.env.ANTHROPIC_API_KEY, undefined);
  assert.equal(options.env.NODE_OPTIONS, undefined);
  assert.equal(options.env.CLAUDE_CODE_OAUTH_TOKEN, 'synthetic-token');
  assert.equal(existsSync(options.cwd), false);
  assert.equal(closed, true);
});

test('environment is an allowlist', () => {
  const env = runtimeEnv({ ANTHROPIC_AUTH_TOKEN: 'no', CLAUDE_CODE_USE_BEDROCK: '1', AWS_ACCESS_KEY_ID: 'no', ANTHROPIC_BASE_URL: 'no', HTTPS_PROXY: 'no', PATH: '/fixture' });
  assert.equal(env.PATH, '/fixture');
  assert.ok(Object.values(env).every(value => value !== 'no'));
});
