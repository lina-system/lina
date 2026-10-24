// SPDX-License-Identifier: Apache-2.0
// Tests for the WhatsApp driver with a synthetic SDK client. The script stands
// for what the phone pushed at link time and for messages that arrive while the
// account stays linked; nothing here contacts WhatsApp.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { whatsappDriver } from './src/whatsapp.ts';
import { Refusal } from './src/protocol.ts';

const ME = '15550000000@s.whatsapp.net';
const ADA = '15551234567@s.whatsapp.net';

function message(id, chat, parts = {}) {
  return {
    id,
    chat_id: chat,
    from: parts.outgoing ? '' : (parts.from ?? chat),
    from_name: parts.name ?? 'Ada',
    timestamp: parts.at ?? '2026-10-24T09:00:00.000Z',
    is_outgoing: Boolean(parts.outgoing),
    type: 'text',
    text: parts.text ?? 'text',
  };
}

class FakeClient {
  constructor(script) {
    this.script = script;
    this.calls = [];
    this.connected = false;
    this.chats = script.chats ?? [];
    this.buffer = new Map();
    for (const entry of script.messages ?? []) this.push(entry.chat_id, entry);
    this.pairingCode = script.pairingCode ?? 'ABCD-EFGH';
  }

  push(chat, value) {
    const list = this.buffer.get(chat) ?? [];
    if (!list.some(entry => entry.id === value.id)) list.push(value);
    list.sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp));
    this.buffer.set(chat, list);
  }

  async login() {
    this.calls.push('login');
    return this;
  }

  async connect() {
    this.calls.push('connect');
    if (this.script.forbidden) {
      throw Object.assign(new Error('Account banned or restricted by WhatsApp.'), { code: 'forbidden' });
    }
    this.connected = true;
  }

  async listChats() {
    this.calls.push('listChats');
    return this.chats;
  }

  async getMessages(chat, limit = 25) {
    this.calls.push('getMessages');
    if (!this.connected) throw Object.assign(new Error('Not connected'), { code: 'not_connected' });
    return (this.buffer.get(chat) ?? []).slice(-limit);
  }

  async getProfile() {
    this.calls.push('getProfile');
    if (!this.connected) throw Object.assign(new Error('Not connected'), { code: 'not_connected' });
    return this.script.me ?? { id: ME, name: 'Me', phone_number: '15550000000' };
  }

  async connectForPairing(phone) {
    this.calls.push('connectForPairing');
    this.pairingPhone = phone;
    this.auth = new Promise((resolve, reject) => {
      this.resolveAuth = resolve;
      this.rejectAuth = reject;
    });
    return { code: this.pairingCode, waitForAuth: () => this.auth };
  }

  async close() {
    this.calls.push('close');
  }

  completePairing() {
    this.connected = true;
    this.resolveAuth?.();
  }
}

function setup(script, options = {}) {
  const configDir = mkdtempSync(join(tmpdir(), 'whatsapp-driver-'));
  if (options.creds !== false) {
    mkdirSync(join(configDir, 'whatsapp'), { recursive: true });
    writeFileSync(join(configDir, 'whatsapp', 'creds.json'), '{}');
  }
  const client = new FakeClient(script);
  const driver = whatsappDriver({ WhatsAppClient: class { constructor() { return client; } } }, { configDir, liveMs: 5, chatsMs: 5 });
  return { configDir, client, driver };
}

async function waitFor(check, what) {
  const deadline = Date.now() + 1000;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise(resolve => setTimeout(resolve, 5));
  }
  assert.fail(`timed out waiting for ${what}`);
}

test('the history pushed at link time is a complete first page and repeats add nothing', async () => {
  const { driver } = setup({
    chats: [{ id: ADA, name: 'Ada', type: 'individual' }],
    messages: [
      message('m-1', ADA, { at: '2026-10-24T09:00:00.000Z', text: 'hello' }),
      message('m-2', ADA, { at: '2026-10-24T09:01:00.000Z', outgoing: true, text: 'hi back' }),
    ],
  });
  assert.equal(await driver.signInState(), 'signed_in');
  const chats = await driver.listChats();
  assert.equal(chats.length, 1);
  assert.equal(chats[0].id, ADA);
  assert.equal(chats[0].name, 'Ada');
  assert.equal(chats[0].kind, 'direct');
  const first = await driver.page(ADA, '');
  assert.equal(first.hasMore, false);
  assert.equal(first.next.length > 0, true);
  assert.deepEqual(first.messages.map(entry => [entry.id, entry.route, entry.author.id]), [
    ['m-1', 'external_participant', ADA],
    ['m-2', 'person', ME],
  ]);
  assert.equal(first.messages[0].text, 'hello');
  assert.equal(first.through, '2026-10-24T09:01:00.000Z');
  const again = await driver.page(ADA, first.next);
  assert.deepEqual(again.messages, []);
  assert.equal(again.next, first.next);
});

test('messages that share a timestamp are all read once', async () => {
  const { driver, client } = setup({
    chats: [{ id: ADA, name: 'Ada', type: 'individual' }],
    messages: [message('m-1', ADA, { at: '2026-10-24T09:00:00.000Z' })],
  });
  await driver.signInState();
  const first = await driver.page(ADA, '');
  client.push(ADA, message('m-2', ADA, { at: '2026-10-24T09:00:00.000Z', text: 'same second' }));
  const second = await driver.page(ADA, first.next);
  assert.deepEqual(second.messages.map(entry => entry.id), ['m-2']);
  assert.deepEqual(second.messages[0].at, '2026-10-24T09:00:00.000Z');
});

test('live messages reach the caller once, with their chat', async () => {
  const { driver, client } = setup({
    chats: [{ id: ADA, name: 'Ada', type: 'individual' }],
    messages: [message('m-1', ADA, { at: '2026-10-24T09:00:00.000Z' })],
  });
  await driver.signInState();
  const { next } = await driver.page(ADA, '');
  assert.ok(next);
  const events = [];
  const before = client.calls.length;
  const stop = driver.live(event => events.push(event));
  // The subscription seeds from what is already buffered; those messages reach
  // Core through the page read, not as live events.
  await waitFor(() => client.calls.slice(before).includes('getMessages'), 'the live seed');
  client.push(ADA, message('m-2', ADA, { at: '2026-10-24T09:02:00.000Z', text: 'live' }));
  await waitFor(() => events.length === 1, 'the live message');
  assert.equal(events[0].event, 'message');
  assert.equal(events[0].chat, ADA);
  assert.equal(events[0].message.id, 'm-2');
  // The polled snapshot repeats the message; the caller sees it once.
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.equal(events.length, 1);
  stop();
  const calls = client.calls.length;
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.equal(client.calls.length, calls, 'the subscription kept polling after stop');
});

test('a chat started after the subscription is followed too', async () => {
  const BOB = '15559876543@s.whatsapp.net';
  const { driver, client } = setup({
    chats: [{ id: ADA, name: 'Ada', type: 'individual' }],
    messages: [message('m-1', ADA)],
  });
  await driver.signInState();
  const events = [];
  const stop = driver.live(event => events.push(event));
  const before = client.calls.length;
  await waitFor(() => client.calls.slice(before).includes('getMessages'), 'the live seed');
  // A conversation that did not exist when the subscription started.
  client.chats.push({ id: BOB, name: 'Bob', type: 'individual' });
  client.push(BOB, message('b-1', BOB, { at: '2026-10-24T09:00:00.000Z', text: 'old' }));
  await waitFor(() => client.calls.slice(before).filter(name => name === 'listChats').length >= 2, 'the chat refresh');
  client.push(BOB, message('b-2', BOB, { at: '2026-10-24T09:03:00.000Z', text: 'new' }));
  await waitFor(() => events.length === 1, 'the message from the new chat');
  assert.equal(events[0].chat, BOB);
  assert.equal(events[0].message.id, 'b-2');
  stop();
});

test('a forbidden account fails sign-in with that cause', async () => {
  const { driver } = setup({ forbidden: true, chats: [] });
  await assert.rejects(driver.signInState(), error => {
    assert.ok(error instanceof Refusal);
    assert.equal(error.code, 'source_forbidden');
    return true;
  });
});

test('a phone number starts pairing and shows the code until the phone confirms', async () => {
  const { driver, client } = setup({ chats: [] }, { creds: false });
  const begin = await driver.beginSignIn();
  assert.equal(begin.state, 'needs_sign_in');
  assert.equal(begin.prompt.key, 'phone');
  const shown = await driver.answer('phone', '+1 (555) 000-0000');
  assert.equal(shown.state, 'needs_sign_in');
  assert.deepEqual(shown.show, { kind: 'code', value: 'ABCD-EFGH' });
  assert.equal(client.pairingPhone, '+1 (555) 000-0000');
  assert.equal(await driver.signInState(), 'needs_sign_in');
  client.completePairing();
  await waitFor(async () => (await driver.signInState()) === 'signed_in', 'the paired account');
});

test('the capability descriptor states the link-time history and live events', () => {
  const { driver } = setup({});
  const capabilities = driver.capabilities();
  assert.equal(capabilities.historyDepth, 'link-time');
  assert.equal(capabilities.liveEvents, true);
  assert.equal(capabilities.edits, false);
  assert.equal(capabilities.deletions, false);
  assert.ok(capabilities.intervalSeconds > 0);
  // A message sent while the add-on is down is missed, so the window is the
  // read interval, not a day the platform cannot replay.
  assert.ok(capabilities.readWindowSeconds <= 2 * capabilities.intervalSeconds);
});

test('an account without a session asks for sign-in without contacting the platform', async () => {
  const { driver, client } = setup({ chats: [] }, { creds: false });
  assert.equal(await driver.signInState(), 'needs_sign_in');
  assert.deepEqual(client.calls, []);
});

test('a revoked link asks for sign-in again', async () => {
  const { driver, client } = setup({ chats: [], me: { id: ME, name: 'Me' } });
  client.getProfile = async () => {
    throw Object.assign(new Error('Logged out'), { code: 'logged_out' });
  };
  assert.equal(await driver.signInState(), 'needs_sign_in');
});

test('a failed socket keeps its cause instead of asking for sign-in', async () => {
  const { driver, client } = setup({ chats: [] });
  client.getProfile = async () => {
    throw Object.assign(new Error('socket closed'), { code: 'connection_closed' });
  };
  await assert.rejects(driver.signInState(), error => {
    assert.equal(error.code, 'connection_closed');
    return true;
  });
});
