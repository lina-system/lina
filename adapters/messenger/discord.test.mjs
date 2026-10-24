// SPDX-License-Identifier: Apache-2.0
// Discord personal through the add-on, over recorded synthetic SDK responses.
// Nothing here contacts Discord, reads a session or installs the SDK: the fake
// SDK answers from a script and records every entry point the driver reaches.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PassThrough } from 'node:stream';
import { serve } from './src/serve.ts';
import { discordDriver } from './src/discord.ts';

const me = { id: '900', username: 'jun', global_name: 'Jun' };

function message(id, at, extra = {}) {
  return { id, channel_id: '20', author: { id: '7', username: 'mina' }, content: `message ${id}`, timestamp: at, ...extra };
}

// fakeSdk records every SDK entry point the driver reaches and answers from a
// script, so a write, read-state or typing call would show up as a recorded
// call rather than a platform effect.
function fakeSdk(script = {}) {
  const state = { token: script.token ?? 'token-1', revoked: false, ...script };
  const calls = [];
  class DiscordCredentialManager {
    constructor(dir) {
      calls.push(['DiscordCredentialManager', dir]);
    }
    async getToken() {
      calls.push(['getToken']);
      return state.token;
    }
    async setToken(token) {
      calls.push(['setToken', token]);
      state.token = token;
    }
  }
  const unreachable = name => async () => {
    calls.push([name]);
    throw new Error(`${name} must not be reachable`);
  };
  class DiscordClient {
    constructor() {
      calls.push(['DiscordClient']);
    }
    async login(credentials) {
      calls.push(['login', credentials]);
      this.token = credentials?.token;
      return this;
    }
    async testAuth() {
      calls.push(['testAuth']);
      if (state.revoked) {
        const error = new Error('401: Unauthorized');
        error.code = '0';
        throw error;
      }
      return me;
    }
    async listServers() {
      calls.push(['listServers']);
      return state.servers ?? [];
    }
    async listChannels(guild) {
      calls.push(['listChannels', guild]);
      return state.channels?.[guild] ?? [];
    }
    async listDMChannels() {
      calls.push(['listDMChannels']);
      return state.dms ?? [];
    }
    async getMessages(chat, limit, options) {
      calls.push(['getMessages', chat, limit, options ?? null]);
      return state.messages ? state.messages(chat, limit, options) : [];
    }
    async listUsers(guild) {
      calls.push(['listUsers', guild]);
      return state.users?.[guild] ?? [];
    }
    sendMessage = unreachable('sendMessage');
    sendTyping = unreachable('sendTyping');
    markRead = unreachable('markRead');
    ackMessage = unreachable('ackMessage');
  }
  const sdk = {
    DiscordClient,
    DiscordCredentialManager,
    loginWithRemoteAuth: async options => {
      calls.push(['loginWithRemoteAuth']);
      options?.onQrUrl?.('https://discord.com/ra/synthetic');
      return state.session ?? { token: 'qr-token', user: me };
    },
  };
  return { sdk, calls, state };
}

function harness(t, sdk) {
  const input = new PassThrough();
  const output = new PassThrough();
  let buffer = '';
  const lines = [];
  output.on('data', chunk => {
    buffer += chunk;
    for (;;) {
      const end = buffer.indexOf('\n');
      if (end < 0) break;
      lines.push(JSON.parse(buffer.slice(0, end)));
      buffer = buffer.slice(end + 1);
    }
  });
  const finished = serve(input, output, {
    open: async () => discordDriver(sdk, { configDir: '/synthetic/state' }),
  }, { platform: 'discord', account: '900' });
  t.after(async () => {
    input.end();
    await finished;
  });
  return {
    async send(value) {
      lines.length = 0;
      input.write(`${JSON.stringify(value)}\n`);
      for (let i = 0; i < 400 && lines.length === 0; i++) await new Promise(resolve => setTimeout(resolve, 5));
      return lines[0];
    },
  };
}

test('guilds, channels and DMs are listed once, as platform snowflakes', async t => {
  const { sdk } = fakeSdk({
    servers: [{ id: '1', name: 'Guild A' }, { id: '2', name: 'Guild B' }],
    channels: {
      1: [
        { id: '10', guild_id: '1', name: 'general', type: 0 },
        { id: '11', guild_id: '1', name: 'voice', type: 2 },
        { id: '12', guild_id: '1', name: 'category', type: 4 },
        { id: '13', guild_id: '1', name: 'thread', type: 11 },
        { id: '14', guild_id: '1', name: 'forum', type: 15 },
      ],
      2: [{ id: '15', guild_id: '2', name: 'news', type: 5 }],
    },
    dms: [
      { id: '20', type: 1, recipients: [{ id: '7', username: 'mina' }] },
      { id: '21', type: 3, name: 'Trip', recipients: [{ id: '7', username: 'mina' }, { id: '8', username: 'sol' }] },
    ],
  });
  const h = harness(t, sdk);
  const frame = await h.send({ id: 'c', op: 'listChats' });
  assert.equal(frame.ok, true);
  assert.deepEqual(frame.result.chats.map(chat => [chat.id, chat.kind]), [
    ['10', 'channel'],
    ['11', 'channel'],
    ['15', 'channel'],
    ['20', 'direct'],
    ['21', 'group'],
  ]);
  assert.equal(frame.result.chats[0].name, 'Guild A #general');
  assert.equal(frame.result.chats[3].name, 'mina');
  assert.equal(frame.result.chats[4].name, 'Trip');
});

test('a DM pages forward by snowflake and reports edits', async t => {
  const scripted = [
    message('1', '2026-10-24T08:00:00.000Z'),
    message('2', '2026-10-24T08:05:00.000Z'),
    message('3', '2026-10-24T08:10:00.000Z'),
    message('4', '2026-10-24T08:15:00.000Z'),
  ];
  const edited = message('5', '2026-10-24T08:20:00.000Z', {
    author: { id: '900', username: 'jun' },
    edited_timestamp: '2026-10-24T08:30:00.000Z',
  });
  const { sdk } = fakeSdk({
    messages: (chat, _limit, options) => {
      assert.equal(chat, '20');
      if (!options) return [4, 3, 2, 1].map(id => scripted[id - 1]);
      assert.deepEqual(options, { around: '4' });
      return [edited, scripted[3]];
    },
  });
  const h = harness(t, sdk);
  const first = await h.send({ id: 'p1', op: 'page', chat: '20', cursor: '' });
  assert.equal(first.ok, true);
  assert.deepEqual(first.result.messages.map(m => m.id), ['1', '2', '3', '4']);
  assert.equal(first.result.messages[0].route, 'external_participant');
  assert.equal(first.result.hasMore, false);
  assert.equal(first.result.next, JSON.stringify({ after: '4' }));

  // A later read reports the window around the newest message Core has, so an
  // edit to a recent message is seen again with its new edited time.
  const second = await h.send({ id: 'p2', op: 'page', chat: '20', cursor: first.result.next });
  assert.equal(second.ok, true);
  assert.deepEqual(second.result.messages.map(m => m.id), ['4', '5']);
  assert.equal(second.result.messages[1].route, 'person');
  assert.equal(second.result.messages[1].edited, '2026-10-24T08:30:00.000Z');
  assert.equal(second.result.hasMore, false);
  assert.equal(second.result.next, JSON.stringify({ after: '5' }));
});

test('a full guild channel window reports more to read', async t => {
  const batch = index => message(String(index), new Date(Date.UTC(2026, 9, 24, 8, 0, index)).toISOString());
  const { sdk } = fakeSdk({
    messages: (_chat, _limit, options) => {
      if (!options) return Array.from({ length: 100 }, (_, i) => batch(101 + i));
      return Array.from({ length: 50 }, (_, i) => batch(201 + i));
    },
  });
  const h = harness(t, sdk);
  const first = await h.send({ id: 'g1', op: 'page', chat: '10', cursor: '' });
  assert.equal(first.result.messages.length, 100);
  const second = await h.send({ id: 'g2', op: 'page', chat: '10', cursor: first.result.next });
  assert.equal(second.ok, true);
  assert.equal(second.result.messages.length, 50);
  assert.equal(second.result.hasMore, true);
  assert.equal(second.result.next, JSON.stringify({ after: '250' }));
});

test('an edit to a message already read is reported again', async t => {
  const original = message('2', '2026-10-24T08:05:00.000Z');
  let edited = false;
  const { sdk } = fakeSdk({
    messages: () => {
      const second = edited ? { ...original, content: 'message 2 (fixed)', edited_timestamp: '2026-10-24T08:20:00.000Z' } : original;
      return [message('1', '2026-10-24T08:00:00.000Z'), second];
    },
  });
  const h = harness(t, sdk);
  const first = await h.send({ id: 'e1', op: 'page', chat: '20', cursor: '' });
  assert.equal(first.result.messages[1].edited, undefined);
  edited = true;
  const second = await h.send({ id: 'e2', op: 'page', chat: '20', cursor: first.result.next });
  assert.equal(second.ok, true);
  const reread = second.result.messages.find(entry => entry.id === '2');
  assert.equal(reread.edited, '2026-10-24T08:20:00.000Z');
  assert.equal(reread.text, 'message 2 (fixed)');
  assert.equal(second.result.next, first.result.next);
});

test('a revoked session reports needs sign-in without a read', async t => {
  const { sdk, calls } = fakeSdk({ token: 'stale-token', revoked: true });
  const h = harness(t, sdk);
  const state = await h.send({ id: 's', op: 'signIn' });
  assert.deepEqual(state, { id: 's', ok: true, result: { state: 'needs_sign_in' } });
  assert.deepEqual(calls.filter(([name]) => name === 'login'), [['login', { token: 'stale-token' }]]);
  assert.ok(!calls.some(([name]) => name === 'getMessages'));
});

test('QR sign-in shows the QR payload and keeps only the session token', async t => {
  const { sdk, calls, state } = fakeSdk({ token: null });
  const h = harness(t, sdk);
  const begin = await h.send({ id: 'b', op: 'beginSignIn' });
  assert.equal(begin.ok, true);
  assert.equal(begin.result.state, 'needs_sign_in');
  assert.deepEqual(begin.result.show, { kind: 'url', value: 'https://discord.com/ra/synthetic' });
  const answer = await h.send({ id: 'a', op: 'answer', prompt: 'qr', value: '' });
  assert.equal(answer.ok, true);
  assert.equal(answer.result.state, 'signed_in');
  assert.deepEqual(calls.filter(([name]) => name === 'setToken'), [['setToken', 'qr-token']]);
  assert.equal(state.token, 'qr-token');
  assert.deepEqual(calls.filter(([name]) => name === 'login'), [['login', { token: 'qr-token' }]]);
});

test('the platform descriptor reaches Core with the measured values', async t => {
  const { sdk } = fakeSdk();
  const h = harness(t, sdk);
  const frame = await h.send({ id: 'cap', op: 'capabilities' });
  assert.deepEqual(frame.result, {
    historyDepth: 'latest window',
    cursorKind: 'snowflake',
    edits: true,
    deletions: false,
    liveEvents: false,
    intervalSeconds: 300,
    readWindowSeconds: 86400,
  });
});

test('members come from the guild or the DM recipients', async t => {
  const { sdk } = fakeSdk({
    servers: [{ id: '1', name: 'Guild A' }],
    channels: { 1: [{ id: '10', guild_id: '1', name: 'general', type: 0 }] },
    dms: [{ id: '20', type: 1, recipients: [{ id: '7', username: 'mina' }] }],
    users: { 1: [{ id: '7', username: 'mina' }, { id: '8', username: 'sol', global_name: 'Sol' }] },
  });
  const h = harness(t, sdk);
  const guild = await h.send({ id: 'm1', op: 'members', chat: '10' });
  assert.deepEqual(guild.result.members, [{ id: '7', name: 'mina' }, { id: '8', name: 'Sol' }]);
  const dm = await h.send({ id: 'm2', op: 'members', chat: '20' });
  assert.deepEqual(dm.result.members, [{ id: '7', name: 'mina' }]);
});

test('no write, read-state or typing call is reachable through the add-on', async t => {
  const { sdk, calls } = fakeSdk();
  const h = harness(t, sdk);
  for (const op of ['hello', 'signIn', 'beginSignIn', 'listChats', 'members', 'whoami', 'live', 'stop']) {
    const args = op === 'members' ? { chat: '20' } : {};
    const frame = await h.send({ id: op, op, ...args });
    assert.equal(frame.ok, true, op);
  }
  for (const name of ['sendMessage', 'sendTyping', 'markRead', 'ackMessage']) {
    assert.ok(!calls.some(([called]) => called === name), name);
  }
  assert.deepEqual(calls.filter(([name]) => name === 'login'), [['login', { token: 'token-1' }]]);
});
