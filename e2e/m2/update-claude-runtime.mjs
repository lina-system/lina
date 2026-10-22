// SPDX-License-Identifier: Apache-2.0
// Synthetic authentication over the production Claude adapter wire boundary.
import { serve } from '../../adapters/claude/adapter.ts';

await serve(process.stdin, process.stdout, async function* (start) {
  yield { type: 'system', subtype: 'init', tools: [], mcp_servers: [], plugins: [] };
  if (process.env.CLAUDE_CODE_OAUTH_TOKEN !== 'synthetic-update-claude-token') {
    yield { type: 'assistant', error: 'authentication_failed' };
    return;
  }
  if (Object.keys(process.env).some(name => /^(OPENAI_|ANTHROPIC_|GEMINI_|GOOGLE_|NODE_OPTIONS$)/.test(name))
      || JSON.stringify(start).includes(process.env.CLAUDE_CODE_OAUTH_TOKEN)) {
    throw new Error('credential boundary failed');
  }
  yield { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'Update credential readiness confirmed.' }] } };
  yield { type: 'result', subtype: 'success', session_id: 'update-fixture', usage: { input_tokens: 1, output_tokens: 1 } };
});
