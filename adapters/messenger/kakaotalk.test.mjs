// SPDX-License-Identifier: Apache-2.0
// KakaoTalk driver contract tests: the pinned SDK is replaced by a recorded,
// LOCO-shaped fixture. Nothing here contacts KakaoTalk, signs in or reads a
// session.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PassThrough } from 'node:stream';
import { serve } from './src/serve.ts';
import { openKakaoTalk } from './src/kakaotalk.ts';
import { fixtureSDK } from './kakaotalk-fixture.ts';
import { READ_METHODS, FORBIDDEN_METHODS } from './src/allowlist.ts';

// A 64-bit log id: past Number.MAX_SAFE_INTEGER, so a number would round it.
const BIG_LOG_ID = '9007199254740993123';

const account = {
  account_id: '7788',
  oauth_token: 'fixture-access-token',
  user_id: '7788',
  device_uuid: 'fixture-device-uuid',
  device_type: 'tablet',
};

const log = (id, text, overrides = {}) => ({
  log_id: id,
  type: 1,
  author_id: 42,
  author_name: 'Someone',
  message: text,
  attachment: null,
  sent_at: Date.parse('2026-10-24T09:00:00Z'),
  ...overrides,
});

function open(script) {
  const dir = mkdtempSync(join(tmpdir(), 'kakaotalk-'));
  const fixture = fixtureSDK(script, dir);
  return { dir, fixture, driver: openKakaoTalk(fixture.sdk, { configDir: dir }) };
}

const signedIn = (extra = {}) => ({
  accounts: { '7788': account },
  profile: { user_id: '7788', nickname: 'Me' },
  ...extra,
});

test('the chat list maps direct and group rooms and keeps the newest 64-bit log id', async () => {
  const { driver, fixture } = open(signedIn({
    chats: [
      { chat_id: '100', type: 11, display_name: 'A friend', title: null, active_members: 2, last_log_id: BIG_LOG_ID },
      { chat_id: '200', type: 10, display_name: 'A room', title: null, active_members: 7, last_log_id: '12' },
    ],
  }));
  const chats = await driver.listChats();
  assert.deepEqual(chats, [
    { id: '100', name: 'A friend', kind: 'direct', lastMessageId: BIG_LOG_ID },
    { id: '200', name: 'A room', kind: 'group', lastMessageId: '12' },
  ]);
  assert.deepEqual(fixture.calls.slice(-3), ['getChats', 'classifyKakaoChat', 'classifyKakaoChat']);
});

test('pages read forward from a log id cursor and finish with the newest time', async () => {
  const { driver } = open(signedIn({
    chats: [{ chat_id: '100', type: 11, display_name: 'A friend', title: null, active_members: 2, last_log_id: BIG_LOG_ID }],
    pages: {
      '': {
        messages: [log('1', 'first'), log('2', 'second', { sent_at: Date.parse('2026-10-24T09:01:00Z') })],
        next_cursor: '2',
        complete: false,
      },
      '2': {
        messages: [log(BIG_LOG_ID, 'third', { author_id: 7788, author_name: 'Me', sent_at: Date.parse('2026-10-24T09:02:00Z') })],
        next_cursor: BIG_LOG_ID,
        complete: true,
      },
    },
  }));
  const first = await driver.page('100', '');
  assert.equal(first.hasMore, true);
  assert.equal(first.next, '2');
  assert.deepEqual(first.messages.map(m => m.id), ['1', '2']);
  assert.equal(first.through, undefined);

  const second = await driver.page('100', first.next);
  assert.equal(second.hasMore, false);
  assert.equal(second.next, BIG_LOG_ID);
  assert.equal(second.through, '2026-10-24T09:02:00.000Z');
  // A message the person wrote is the person's own route; anything else is an
  // external participant, never a guess from the display name.
  assert.equal(second.messages[0].route, 'person');
  assert.equal(first.messages[0].route, 'external_participant');

  const again = await driver.page('100', second.next);
  assert.deepEqual(again, { messages: [], deletions: [], next: BIG_LOG_ID, hasMore: false, through: undefined });
});

test('an empty message keeps an empty body with its type and attachment metadata', async () => {
  const { driver } = open(signedIn({
    chats: [{ chat_id: '100', type: 11, display_name: 'A friend', title: null, active_members: 2, last_log_id: '9' }],
    pages: {
      '': {
        messages: [
          log('1', '', { type: 18, attachment: { k: 'file-key', name: 'report.pdf', mt: 'application/pdf', s: 2048 } }),
          log('2', '', { type: 2, attachment: { k: 'photo-key', name: 'photo.jpg', mt: 'image/jpeg', s: 4096 } }),
          log('3', 'a reply', { type: 26, attachment: { attach_only: false, src_logId: '1', src_userId: '42', src_message: 'first', src_type: 18 } }),
        ],
        next_cursor: null,
        complete: true,
      },
    },
  }));
  const page = await driver.page('100', '');
  assert.equal(page.messages[0].text, '');
  assert.deepEqual(page.messages[0].attachments, [{ id: 'file-key', name: 'report.pdf', mimeType: 'application/pdf', size: 2048 }]);
  assert.equal(page.messages[1].text, '');
  assert.equal(page.messages[1].attachments[0].mimeType, 'image/jpeg');
  assert.equal(page.messages[2].replyTo, '1');
  assert.equal(page.messages[2].attachments, undefined);
});

test('the driver reports no edits, deletions or write verbs', async () => {
  const { driver, fixture } = open(signedIn());
  const capabilities = driver.capabilities();
  assert.equal(capabilities.edits, false);
  assert.equal(capabilities.deletions, false);
  assert.equal(capabilities.liveEvents, true);
  assert.equal(capabilities.cursorKind, 'log-id');
  assert.equal(capabilities.readWindowSeconds, 172_800);
  assert.equal(capabilities.writes, undefined);
  // Nothing in a capability descriptor reaches the platform.
  assert.deepEqual(fixture.calls, ['KakaoCredentialManager']);
});

test('live pushes carry the message a page would read', async () => {
  const { driver, fixture } = open(signedIn({
    chats: [{ chat_id: '100', type: 11, display_name: 'A friend', title: null, active_members: 2, last_log_id: BIG_LOG_ID }],
  }));
  const events = [];
  const stop = driver.live(event => events.push(event));
  await new Promise(resolve => setTimeout(resolve, 5));
  fixture.deliver({
    method: 'MSG',
    body: {
      chatId: '100',
      chatLog: {
        logId: BIG_LOG_ID, type: 1, authorId: 42, authorName: 'Someone',
        message: 'live', attachment: null, sendAt: Date.parse('2026-10-24T10:00:00Z'),
      },
    },
  });
  fixture.deliver({ method: 'CHANGESVR', body: {} });
  fixture.deliver({ method: 'MSG', body: { chatLog: { logId: '77', type: 1, authorId: 42, message: 'no chat', sendAt: 1 } } });
  stop();
  assert.equal(events.length, 1);
  assert.equal(events[0].chat, '100');
  assert.equal(events[0].message.id, BIG_LOG_ID);
  assert.equal(events[0].message.at, '2026-10-24T10:00:00.000Z');
  assert.equal(events[0].message.text, 'live');
});

test('sign-in runs on the tablet slot through the passcode the person confirms on the phone', async () => {
  const { driver, fixture } = open({
    login: {
      attempts: [{ authenticated: false, next_action: 'provide_passcode' }, { authenticated: true, account_id: '7788', user_id: '7788' }],
      passcode: '4821',
    },
    profile: { user_id: '7788', nickname: 'Me' },
    chats: [],
  });
  assert.equal(await driver.signInState(), 'needs_sign_in');

  let step = await driver.beginSignIn();
  assert.deepEqual(step, { state: 'needs_sign_in', prompt: { key: 'email', kind: 'text', text: 'KakaoTalk email' } });
  step = await driver.answer('email', 'person@example.test');
  assert.deepEqual(step.prompt, { key: 'password', kind: 'secret', text: 'KakaoTalk password' });
  step = await driver.answer('password', 'not-stored');
  assert.deepEqual(step, { state: 'needs_sign_in', show: { kind: 'code', value: '4821' } });
  // The registration finishes in the background while the person confirms the
  // code on their phone.
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(await driver.signInState(), 'signed_in');
  assert.deepEqual(await driver.whoami(), { id: '7788', name: 'Me' });
  assert.ok(fixture.calls.includes('registerDevice'));
  assert.deepEqual(fixture.calls.filter(call => call === 'attemptLogin').length, 2);
});

test('a stored session the platform rejects asks for sign-in instead of failing', async () => {
  const { driver } = open(signedIn({ expire: true }));
  assert.equal(await driver.signInState(), 'needs_sign_in');
});

test('an unknown chat class refuses the source rather than guessing its kind', async () => {
  const { driver } = open(signedIn({
    chats: [{ chat_id: '100', type: 'SomeFutureChat', display_name: 'Future', title: null, active_members: 2, last_log_id: '9' }],
  }));
  await assert.rejects(driver.listChats(), error => error.code === 'upstream_changed');
});

// The whole add-on boundary: a line protocol request, the driver, and the
// recorded SDK. Every SDK call the driver made must be one the allowlist names.
test('no SDK method outside the read allowlist is ever reached', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'kakaotalk-boundary-'));
  writeFileSync(join(dir, 'script.json'), JSON.stringify(signedIn({
    chats: [{ chat_id: '100', type: 11, display_name: 'A friend', title: null, active_members: 2, last_log_id: BIG_LOG_ID }],
    pages: {
      '': { messages: [log('1', 'first')], next_cursor: null, complete: true },
    },
    members: [{ user_id: '42', nickname: 'Someone' }],
  })));
  const fixture = fixtureSDK(signedIn({
    chats: [{ chat_id: '100', type: 11, display_name: 'A friend', title: null, active_members: 2, last_log_id: BIG_LOG_ID }],
    pages: {
      '': { messages: [log('1', 'first')], next_cursor: null, complete: true },
    },
    members: [{ user_id: '42', nickname: 'Someone' }],
  }), dir);
  const input = new PassThrough(), output = new PassThrough();
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
  const done = serve(input, output, { open: async () => openKakaoTalk(fixture.sdk, { configDir: dir }) }, { platform: 'kakaotalk', account: '7788' });
  t.after(async () => {
    input.end();
    await done;
  });
  const send = async request => {
    lines.length = 0;
    input.write(JSON.stringify(request) + '\n');
    for (let i = 0; i < 400 && lines.length === 0; i++) await new Promise(resolve => setTimeout(resolve, 5));
    return lines[0];
  };
  for (const request of [
    { id: 'a', op: 'signIn' },
    { id: 'c', op: 'listChats' },
    { id: 'd', op: 'page', chat: '100', cursor: '' },
    { id: 'e', op: 'members', chat: '100' },
    { id: 'f', op: 'whoami' },
  ]) {
    const frame = await send(request);
    assert.equal(frame.ok, true, request.op);
    if (request.op === 'signIn') {
      // The sign-in reply carries the platform's own descriptor, so Core learns
      // its schedule and read window without another round trip.
      assert.equal(frame.result.capabilities.cursorKind, 'log-id');
      assert.equal(frame.result.capabilities.edits, false);
      assert.equal(frame.result.capabilities.readWindowSeconds, 172_800);
    }
  }
  // Writes, read-state changes and token extraction have no route at all.
  for (const op of ['sendMessage', 'markRead', 'getReadWatermarks', 'extractToken', 'leaveChat', 'sendAttachment']) {
    const frame = await send({ id: 'x', op });
    assert.deepEqual(frame, { id: null, ok: false, error: 'unknown_operation' }, op);
  }
  const allowed = new Set(READ_METHODS.kakaotalk);
  for (const call of fixture.calls) {
    assert.ok(allowed.has(call), `${call} is outside the KakaoTalk read allowlist`);
    assert.ok(!FORBIDDEN_METHODS.includes(call), `${call} is forbidden`);
  }
  assert.ok(fixture.calls.includes('getMessagePage'));
});

test('the phone confirmation keeps showing its code until the tablet is registered', async () => {
  const { driver } = open({
    login: {
      attempts: [{ authenticated: false, next_action: 'provide_passcode' }, { authenticated: true, account_id: '7788', user_id: '7788' }],
      passcode: '4821',
      registration: { delayMs: 40 },
    },
    profile: { user_id: '7788', nickname: 'Me' },
    chats: [],
  });
  assert.deepEqual(await driver.beginSignIn(), {
    state: 'needs_sign_in', prompt: { key: 'email', kind: 'text', text: 'KakaoTalk email' },
  });
  assert.equal((await driver.answer('email', 'person@example.com')).prompt.key, 'password');
  const shown = await driver.answer('password', 'synthetic-password');
  assert.equal(shown.state, 'needs_sign_in');
  assert.deepEqual(shown.show, { kind: 'code', value: '4821' });
  // A repeated step while the phone confirms shows the same code, and a second
  // answer is refused rather than starting another login.
  assert.deepEqual((await driver.beginSignIn()).show, { kind: 'code', value: '4821' });
  await assert.rejects(driver.answer('password', 'again'), /sign-in in progress/);
  for (let i = 0; i < 40 && (await driver.signInState()) !== 'signed_in'; i++) {
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  assert.equal((await driver.beginSignIn()).state, 'signed_in');
});
