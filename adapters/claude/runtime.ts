// SPDX-License-Identifier: Apache-2.0
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Runtime } from './adapter.ts';

// Keep the subscription credential, but never competing keys, cloud routes,
// telemetry settings or Node injection variables inherited from the caller.
export function runtimeEnv(source: NodeJS.ProcessEnv): Record<string, string> {
  const env: Record<string, string> = {};
  for (const key of ['PATH', 'HOME', 'USERPROFILE', 'SystemRoot', 'TMPDIR', 'TMP', 'TEMP', 'CLAUDE_CODE_OAUTH_TOKEN']) {
    if (source[key] !== undefined) env[key] = source[key];
  }
  return { ...env, DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1', DISABLE_AUTOUPDATER: '1' };
}

export function sdkRuntime(sdk: any, z: any, env = process.env): Runtime {
  return async function* (start, call, signal) {
    const cwd = await mkdtemp(join(tmpdir(), 'lina-claude-'));
    const abortController = new AbortController();
    const abort = () => abortController.abort();
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) abort();
    let query;
    try {
      const definitions = start.tools.map(decl => {
        const schema = z.fromJSONSchema(decl.parameters);
        if (!schema.shape) throw new Error('unsupported tool schema');
        return sdk.tool(decl.name, decl.description ?? '', schema.shape, (args: unknown) => call(decl.name, args));
      });
      query = sdk.query({
        prompt: JSON.stringify({ conversation: start.items }),
        options: {
          systemPrompt: start.system, model: start.model, effort: start.effort || undefined,
          cwd, env: runtimeEnv(env), abortController,
          tools: [], allowedTools: start.tools.map(t => `mcp__lina__${t.name}`),
          mcpServers: definitions.length ? { lina: sdk.createSdkMcpServer({ name: 'lina', version: '1.0.0', tools: definitions }) } : {},
          strictMcpConfig: true, settingSources: [], settings: {}, plugins: [],
          persistSession: false, includePartialMessages: true,
          canUseTool: async () => ({ behavior: 'deny', message: 'Only LINA MCP tools are available.' }),
        },
      });
      yield* query;
    } finally {
      abortController.abort();
      query?.close();
      signal.removeEventListener('abort', abort);
      await rm(cwd, { recursive: true, force: true });
    }
  };
}
