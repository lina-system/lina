// SPDX-License-Identifier: Apache-2.0
// The LINE driver against recorded synthetic SDK responses. Nothing here
// imports the pinned SDK, contacts LINE or uses a personal account.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { lineDriver } from './src/line.ts';

const credentials = {
  account_id: 'me',
  auth_token: 'synthetic-token',
  device: 'ANDROIDSECONDARY',
  created_at: '2026-10-24T09:00:00Z',
  updated_at: '2026-10-24T09:00:00Z',
};

function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

// sdkWith builds the pinned module's shape. Every method is recorded so a test
// can prove which SDK calls the driver makes.
function sdkWith(behaviour = {}) {
  const calls = { credentialDirs: [], chats: undefined, messages: [], closed: 0 };
  class CredentialManager {
    constructor(dir) {
      calls.credentialDirs.push(dir);
    }
    async getAccount() {
      return behaviour.credentials ?? null;
    }
  }
  class LineClient {
    async loginWithQR(options) {
      if (behaviour.loginQR) return behaviour.loginQR(options);
      throw new Error('unexpected QR sign-in');
    }
    async login() {
      if (behaviour.login) return behaviour.login(credentials);
      return this;
    }
    async getProfile() {
      return behaviour.profile ?? { mid: 'me', display_name: 'Me' };
    }
    async getChats(options) {
      calls.chats = options;
      return behaviour.chats ?? [];
    }
    async getMessages(chat, options) {
      calls.messages.push({ chat, options });
      return behaviour.messages ?? [];
    }
    async *streamEvents(signal) {
      yield* behaviour.events ?? [];
      if (behaviour.streamError) throw behaviour.streamError;
      if (behaviour.streamHolds) await new Promise(resolve => signal.addEventListener('abort', resolve));
    }
    close() {
      calls.closed++;
    }
  }
  return { sdk: { LineClient, LineCredentialManager: CredentialManager }, calls };
}

const at = offset => new Date(Date.UTC(2026, 9, 24, 9, offset, 0)).toISOString();

test('chats and newest messages map to read records, oldest first', async () => {
  const { sdk, calls } = sdkWith({
    credentials,
    chats: [
      { chat_id: 'dm-1', type: 'user', display_name: 'A friend' },
      { chat_id: 'group-1', type: 'group', display_name: 'Group' },
      { chat_id: 'square-1', type: 'square', display_name: 'Square' },
    ],
    messages: [
      { message_id: '30', chat_id: 'dm-1', author_id: 'you', author_name: 'You', text: 'newest', content_type: 'NONE', sent_at: at(3) },
      { message_id: '10', chat_id: 'dm-1', author_id: 'me', author_name: 'Me', text: 'oldest', content_type: 'NONE', sent_at: at(1) },
      { message_id: '20', chat_id: 'dm-1', author_id: 'you', author_name: 'You', text: 'middle', content_type: 'NONE', sent_at: at(2) },
    ],
  });
  const driver = lineDriver(sdk, { configDir: '/state/plugins/messenger-addon/account-1' });

  assert.equal(await driver.signInState(), 'signed_in');
  assert.deepEqual(calls.credentialDirs, ['/state/plugins/messenger-addon/account-1']);

  const chats = await driver.listChats();
  assert.deepEqual(chats.map(chat => [chat.id, chat.kind]), [['dm-1', 'direct'], ['group-1', 'group'], ['square-1', 'channel']]);

  const page = await driver.page('dm-1', '');
  assert.deepEqual(page.messages.map(message => message.id), ['10', '20', '30']);
  assert.equal(page.messages[0].route, 'person');
  assert.equal(page.messages[1].route, 'external_participant');
  assert.equal(page.next, '');
  assert.equal(page.hasMore, false);
  assert.equal(page.through, at(3));
  assert.deepEqual(page.coverage, { historyDepth: 'newest-N', cursorSurvivesRestart: false });

  assert.deepEqual(driver.capabilities(), {
    historyDepth: 'newest-N',
    cursorKind: 'none',
    edits: false,
    deletions: false,
    liveEvents: true,
    intervalSeconds: 300,
    readWindowSeconds: 0,
  });
});

test('a stale cursor re-reads the newest page and returns no cursor', async () => {
  const { sdk, calls } = sdkWith({
    credentials,
    messages: [
      { message_id: '30', chat_id: 'dm-1', author_id: 'you', text: 'c', content_type: 'NONE', sent_at: at(3) },
      { message_id: '20', chat_id: 'dm-1', author_id: 'you', text: 'b', content_type: 'NONE', sent_at: at(2) },
    ],
  });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });

  const beforeRestart = await driver.page('dm-1', '');
  const afterRestart = await driver.page('dm-1', 'cursor-from-the-previous-process');
  assert.deepEqual(beforeRestart.messages.map(message => message.id), afterRestart.messages.map(message => message.id));
  assert.equal(afterRestart.next, '');
  assert.equal(afterRestart.hasMore, false);
  assert.equal(calls.messages.length, 2);
});

test('a decryption failure is flagged and carries no text', async () => {
  const { sdk } = sdkWith({
    credentials,
    messages: [
      { message_id: '40', chat_id: 'dm-1', author_id: 'you', text: 'readable', content_type: 'NONE', sent_at: at(4) },
      { message_id: '41', chat_id: 'dm-1', author_id: 'you', text: null, decryption_error: { code: 'missing_e2ee_key', message: 'no key' }, content_type: 'NONE', sent_at: at(5) },
      { message_id: '42', chat_id: 'dm-1', author_id: 'you', text: null, decryption_error: { code: 'decrypt_failed', message: 'bad key' }, content_type: 'NONE', sent_at: at(6) },
    ],
  });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });
  const page = await driver.page('dm-1', '');
  const flagged = page.messages.filter(message => message.decryptionFailed);
  assert.deepEqual(flagged.map(message => message.id), ['41', '42']);
  for (const message of flagged) assert.equal(message.text, '');
  assert.equal(page.messages[0].decryptionFailed, undefined);
  assert.equal(page.messages[0].text, 'readable');
});

test('QR and PIN sign-in shows each step and then signs in', async () => {
  const login = deferred();
  let steps;
  const { sdk } = sdkWith({
    credentials: null,
    loginQR(options) {
      steps = options;
      return login.promise;
    },
  });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });

  assert.equal(await driver.signInState(), 'needs_sign_in');
  const pending = driver.beginSignIn();
  await new Promise(resolve => setTimeout(resolve, 0));
  steps.onQRUrl('https://line.example/qr/synthetic');
  assert.deepEqual(await pending, { state: 'needs_sign_in', show: { kind: 'url', value: 'https://line.example/qr/synthetic' } });

  // LINE asks for the PIN after the person scans the code; it is typed into
  // the phone, so the add-on shows it and keeps nothing.
  // Asking again re-shows the current step and does not start a second flow.
  assert.deepEqual(await driver.beginSignIn(), { state: 'needs_sign_in', show: { kind: 'url', value: 'https://line.example/qr/synthetic' } });

  steps.onPincode('846210');
  assert.deepEqual(await driver.beginSignIn(), { state: 'needs_sign_in', show: { kind: 'code', value: '846210' } });

  login.resolve({ authenticated: true, account_id: 'me', display_name: 'Me' });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(await driver.beginSignIn(), { state: 'signed_in' });
});

test('a revoked session needs sign-in instead of failing the source', async () => {
  const { sdk } = sdkWith({
    credentials,
    login: async () => {
      throw Object.assign(new Error('AUTHENTICATION_FAILED'), { code: 'AUTHENTICATION_FAILED' });
    },
  });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });
  assert.equal(await driver.signInState(), 'needs_sign_in');
});

test('live events carry the same record shape as a page', async () => {
  const { sdk } = sdkWith({
    credentials,
    events: [
      { kind: 'message', message: { raw: { id: '50', createdTime: Date.UTC(2026, 9, 24, 9, 5) }, from: { id: 'you' }, to: { id: 'me' }, text: null, decryption_error: { code: 'missing_e2ee_key' } } },
    ],
    streamHolds: true,
  });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });
  const events = [];
  const delivered = deferred();
  const ends = [];
  const stop = driver.live(
    event => {
      events.push(event);
      delivered.resolve();
    },
    error => ends.push(error),
  );
  await delivered.promise;
  stop();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(events.length, 1);
  assert.equal(events[0].event, 'message');
  assert.equal(events[0].message.id, '50');
  assert.equal(events[0].message.decryptionFailed, true);
  assert.equal(events[0].message.text, '');
  assert.deepEqual(ends, [], 'a stopped subscription reports no end');
});

// liveEnd subscribes and resolves with the one error the driver reports when
// the subscription ends without being stopped.
async function liveEnd(driver) {
  const ended = deferred();
  const events = [];
  driver.live(
    event => events.push(event),
    error => ended.resolve(error),
  );
  return { error: await ended.promise, events };
}

test('a live stream that fails reports its end with the cause', { timeout: 10_000 }, async () => {
  const lost = Object.assign(new Error('socket hang up at /synthetic/host'), { code: 'ECONNRESET' });
  const { sdk } = sdkWith({
    credentials,
    events: [{ kind: 'message', message: { raw: { id: '51', createdTime: Date.UTC(2026, 9, 24, 9, 6) }, from: { id: 'you' }, text: 'before' } }],
    streamError: lost,
  });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });
  const { error, events } = await liveEnd(driver);
  assert.equal(events.length, 1);
  assert.equal(error, lost);
  assert.equal(await driver.signInState(), 'signed_in', 'a network loss is not a sign-in problem');
});

test('a live stream LINE revokes reports needs_sign_in and drops the session', { timeout: 10_000 }, async () => {
  let logins = 0;
  const { sdk } = sdkWith({
    credentials,
    login: async () => {
      logins++;
      if (logins > 1) throw Object.assign(new Error('AUTHENTICATION_FAILED'), { code: 'AUTHENTICATION_FAILED' });
    },
    streamError: Object.assign(new Error('AUTHENTICATION_FAILED'), { code: 'AUTHENTICATION_FAILED' }),
  });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });
  const { error } = await liveEnd(driver);
  assert.equal(error.code, 'needs_sign_in');
  assert.equal(await driver.signInState(), 'needs_sign_in');
});

test('a live stream that closes on its own reports its end', { timeout: 10_000 }, async () => {
  const { sdk } = sdkWith({ credentials });
  const driver = lineDriver(sdk, { configDir: '/synthetic' });
  const { error } = await liveEnd(driver);
  assert.equal(error.code, 'live_ended');
});

test('a live subscription that cannot open reports needs_sign_in', { timeout: 10_000 }, async () => {
  const { sdk } = sdkWith({});
  const driver = lineDriver(sdk, { configDir: '/synthetic' });
  const { error } = await liveEnd(driver);
  assert.equal(error.code, 'needs_sign_in');
});
