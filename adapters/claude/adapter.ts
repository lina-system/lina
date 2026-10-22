// SPDX-License-Identifier: Apache-2.0
import type { Readable, Writable } from 'node:stream';
import { randomUUID } from 'node:crypto';

export type Start = { type: 'start'; id: string; system: string; model: string; effort?: string; items: unknown[]; tools: { name: string; description?: string; parameters: Record<string, unknown> }[] };
export type ToolResult = { content: { type: string; text?: string }[]; isError?: boolean };
export type Runtime = (start: Start, call: (name: string, args: unknown) => Promise<ToolResult>, signal: AbortSignal) => AsyncIterable<any>;
const maxFrame = 8 * 1024 * 1024;
const identifier = (s: unknown): s is string => typeof s === 'string' && /^[A-Za-z0-9_.:-]{1,128}$/.test(s);
class Refusal extends Error {}

function validateStart(m: any): asserts m is Start {
  if (typeof m.system !== 'string' || typeof m.model !== 'string' || !m.model
      || !Array.isArray(m.items) || !Array.isArray(m.tools)
      || (m.effort !== undefined && m.effort !== '' && !['low', 'medium', 'high', 'xhigh', 'max'].includes(m.effort))) throw new Refusal('invalid_start');
  const names = new Set();
  for (const tool of m.tools) {
    if (!tool || !/^[A-Za-z][A-Za-z0-9_]{0,63}$/.test(tool.name) || names.has(tool.name)
        || tool.parameters?.type !== 'object') throw new Refusal('invalid_start');
    names.add(tool.name);
  }
}
function checkInit(event: any, start: Start) {
  const tools = new Set(start.tools.map(t => `mcp__lina__${t.name}`));
  if (!Array.isArray(event.tools) || event.tools.some((name: string) => !tools.has(name))
      || !Array.isArray(event.mcp_servers) || event.mcp_servers.some((s: any) => !tools.size || s.name !== 'lina' || s.source !== 'sdk' || s.status !== 'connected')
      || !Array.isArray(event.plugins) || event.plugins.length
      || event.skills?.length || event.agents?.length) throw new Refusal('unexpected_runtime');
}
function usageOf(event: any) {
  const usage: Record<string, number> = {};
  const mapping = { input: 'input_tokens', output: 'output_tokens', cached: 'cache_read_input_tokens', cache_write: 'cache_creation_input_tokens' };
  for (const [key, field] of Object.entries(mapping)) {
    const value = event.usage?.[field];
    if (value !== undefined) {
      if (!Number.isSafeInteger(value) || value < 0) throw new Refusal('invalid_runtime');
      usage[key] = value;
    }
  }
  return usage;
}

// The only executable boundary is Runtime. Core owns each tool's authority and result.
export async function serve(input: Readable, output: Writable, runtime: Runtime): Promise<void> {
  const turns = new Map<string, { abort: AbortController; done: Promise<void>; pending: Map<string, (value: ToolResult) => void> }>();
  const used = new Set<string>();
  const send = (message: object) => { output.write(JSON.stringify(message) + '\n'); };
  async function run(start: Start, state: { abort: AbortController; pending: Map<string, (value: ToolResult) => void> }) {
    let initialized = false;
    let terminal = false;
    const emit = (message: object) => send({ ...message, id: start.id });
    const call = async (name: string, args: unknown): Promise<ToolResult> => {
      if (!initialized || !start.tools.some(t => t.name === name) || state.abort.signal.aborted) throw new Refusal('unexpected_tool');
      const call_id = randomUUID();
      return new Promise((resolve, reject) => {
        const abort = () => { state.pending.delete(call_id); reject(new Refusal('cancelled')); };
        state.abort.signal.addEventListener('abort', abort, { once: true });
        state.pending.set(call_id, result => {
          state.abort.signal.removeEventListener('abort', abort);
          state.pending.delete(call_id); resolve(result);
        });
        emit({ type: 'tool_call', call_id, name, arguments: args });
      });
    };
    try {
      for await (const event of runtime(start, call, state.abort.signal)) {
        if (state.abort.signal.aborted) throw new Refusal('cancelled');
        if (event.type === 'system' && event.subtype === 'init') {
          if (initialized) throw new Refusal('unexpected_runtime');
          checkInit(event, start); initialized = true;
          emit({ type: 'init', tools: event.tools, mcp_servers: event.mcp_servers, plugins: event.plugins });
          continue;
        }
        if (!initialized) throw new Refusal('missing_init');
        if (event.type === 'stream_event' && event.event?.type === 'content_block_delta' && event.event.delta?.type === 'text_delta') {
          emit({ type: 'delta', text: event.event.delta.text });
        } else if (event.type === 'assistant') {
          if (event.error) throw new Refusal(event.error === 'authentication_failed' ? 'needs_sign_in' : 'runtime_failed');
          emit({ type: 'item_done', item: event.message });
        } else if (event.type === 'rate_limit_event') {
          emit({ type: 'limit', limit: event.rate_limit_info });
        } else if (event.type === 'result') {
          emit({ type: 'usage', usage: usageOf(event) });
          const cost = event.total_cost_usd;
          const cost_estimate = Number.isFinite(cost) && cost >= 0 ? { currency: 'USD', micros: Math.round(cost * 1e6) } : undefined;
          emit({ type: 'terminal', reason: event.subtype === 'success' && !event.is_error ? 'completed' : 'failed', session_id: event.session_id, cost_estimate });
          terminal = true;
          break;
        }
      }
      if (!terminal) throw new Refusal('incomplete_runtime');
    } catch (error) {
      if (!state.abort.signal.aborted) emit({ type: 'error', code: error instanceof Refusal ? error.message : 'runtime_failed' });
      if (!terminal) emit({ type: 'terminal', reason: state.abort.signal.aborted ? 'cancelled' : 'failed' });
    } finally {
      state.abort.abort();
      turns.delete(start.id);
    }
  }
  const dispatch = (m: any) => {
    if (!m || !identifier(m.id)) throw new Refusal('invalid_message');
    if (m.type === 'ping') { send({ type: 'pong', id: m.id }); return; }
    if (m.type === 'start') {
      validateStart(m);
      if (used.has(m.id) || turns.size >= 2) throw new Refusal('turn_unavailable');
      used.add(m.id);
      const state = { abort: new AbortController(), pending: new Map<string, (value: ToolResult) => void>(), done: Promise.resolve() };
      turns.set(m.id, state); state.done = run(m, state); return;
    }
    const turn = turns.get(m.id);
    if (!turn) throw new Refusal('unknown_turn');
    if (m.type === 'cancel') { turn.abort.abort(); return; }
    if (m.type === 'tool_result') {
      const resolve = turn.pending.get(m.call_id);
      if (!resolve || !Array.isArray(m.result?.content)) throw new Refusal('invalid_tool_result');
      resolve(m.result); return;
    }
    throw new Refusal('invalid_message');
  };
  let buffer = Buffer.alloc(0);
  try {
    for await (const chunk of input) {
      buffer = Buffer.concat([buffer, Buffer.from(chunk)]);
      for (;;) {
        const end = buffer.indexOf(10);
        if (end < 0) break;
        if (end > maxFrame) throw new Refusal('frame_too_large');
        const line = buffer.subarray(0, end).toString('utf8'); buffer = buffer.subarray(end + 1);
        let message;
        try { message = JSON.parse(line); dispatch(message); }
        catch (error) { send({ type: 'error', id: identifier(message?.id) ? message.id : null, code: error instanceof Refusal ? error.message : 'invalid_message' }); }
      }
      if (buffer.length > maxFrame) throw new Refusal('frame_too_large');
    }
    if (buffer.length) throw new Refusal('truncated_frame');
  } catch (error) {
    send({ type: 'error', id: null, code: error instanceof Refusal ? error.message : 'input_failed' });
  } finally {
    const active = [...turns.values()];
    for (const turn of active) turn.abort.abort();
    await Promise.all(active.map(t => t.done));
  }
}
