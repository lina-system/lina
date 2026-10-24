// SPDX-License-Identifier: Apache-2.0

// LINE's driver for the messenger add-on. LINE is signed in as a secondary
// Android device with a QR code and a PIN; the session and the Letter Sealing
// keys stay in the account's data directory (0700, outside backups). The driver
// reads only: chats, the newest messages of a chat and live messages. LINE
// serves no cursor that survives a restart, so every page asks for the newest
// messages again and Core drops what it already stored by message id.
//
// A message LINE reports as encrypted but undecryptable is passed on as a
// record that carries the decryption-failed flag and no text; it is never an
// empty message. Sign-in is driven from the add-on's own flow: the first call
// shows the QR code, a later call shows the PIN the person types into the
// phone.
import { cap, clamp, LIMITS, type DriverOptions } from './driver.ts';
import { Refusal, type Author, type Capabilities, type Chat, type Driver, type LiveEvent, type Message, type Page, type SignInStep, type SignInState } from './protocol.ts';

// NEWEST is how many messages one read covers. LINE serves the newest messages
// of a chat backwards and offers no position that outlives the session.
const NEWEST = 200;
const INTERVAL_SECONDS = 300;

type Credentials = { account_id: string; auth_token: string; device: string };
type Profile = { mid: string; display_name?: string };
type DecryptionError = { code: string; message: string };
type LineChat = { chat_id: string; type: string; display_name?: string };
type LineMessage = {
  message_id: string;
  author_id?: string;
  author_name?: string;
  text?: string | null;
  decryption_error?: DecryptionError;
  sent_at?: string;
};
type LoginResult = { authenticated?: boolean; account_id?: string; display_name?: string };
type QRLogin = { device?: string; onQRUrl: (url: string) => void; onPincode: (pin: string) => void };
type RawMessage = {
  raw: { id?: unknown; createdTime?: unknown };
  from: { id?: unknown };
  text?: string | null;
  decryption_error?: DecryptionError;
};
type RawEvent = { kind: 'message'; message: RawMessage } | { kind: 'event' };

type Client = {
  loginWithQR(options: QRLogin): Promise<LoginResult>;
  login(credentials?: Credentials): Promise<unknown>;
  getProfile(): Promise<Profile>;
  getChats(options?: { limit?: number }): Promise<LineChat[]>;
  getMessages(chat: string, options?: { count?: number }): Promise<LineMessage[]>;
  streamEvents(signal: AbortSignal): AsyncGenerator<RawEvent, void, unknown>;
  close(): void;
};

type CredentialManager = { getAccount(accountId?: string): Promise<Credentials | null> };

// LineSdk is the pinned module's line entry point: the client and its
// credential manager. No other part of the SDK is reachable from here.
export type LineSdk = {
  LineClient: new (credentials?: CredentialManager) => Client;
  LineCredentialManager: new (configDir?: string) => CredentialManager;
};

// isLineSdk checks the pinned module's two entry points before any of them is
// called, so a changed export fails as upstream changed instead of throwing
// inside the request loop.
function isLineSdk(value: unknown): value is LineSdk {
  return (
    !!value &&
    typeof value === 'object' &&
    'LineClient' in value &&
    typeof value.LineClient === 'function' &&
    'LineCredentialManager' in value &&
    typeof value.LineCredentialManager === 'function'
  );
}

const isoTime = (value: unknown): string | null => {
  if (typeof value !== 'string' && typeof value !== 'number') return null;
  const parsed = typeof value === 'number' ? value : Date.parse(value);
  return Number.isFinite(parsed) ? new Date(parsed).toISOString() : null;
};

// A revoked token comes back as a LINE authentication failure. A network error
// is not a sign-in problem, so only the authentication words count.
function revoked(error: unknown): boolean {
  let code = '';
  if (error && typeof error === 'object' && 'code' in error && typeof error.code === 'string') code = error.code;
  const message = error instanceof Error ? error.message : '';
  return /AUTHENTICATION_FAILED|NOT_AUTHORIZED|TOKEN_EXPIRED|INVALID_TOKEN|not_authenticated|session expired|invalid token/i.test(`${code} ${message}`);
}

function kindOf(type: string): string {
  switch (type) {
    case 'user':
      return 'direct';
    case 'square':
      return 'channel';
    default:
      return 'group';
  }
}

// Message ids are 64-bit LINE message ids, so numeric order compares as such.
// Pages are oldest first, and the ids—not the response position—decide which
// messages an earlier read already stored.
function order(left: { at: string; id: string }, right: { at: string; id: string }): number {
  const byTime = Date.parse(left.at) - Date.parse(right.at);
  if (byTime !== 0) return byTime;
  if (/^\d+$/.test(left.id) && /^\d+$/.test(right.id)) {
    const a = BigInt(left.id);
    const b = BigInt(right.id);
    return a < b ? -1 : a > b ? 1 : 0;
  }
  return left.id < right.id ? -1 : left.id > right.id ? 1 : 0;
}

export function lineDriver(sdk: unknown, options: DriverOptions): Driver {
  if (!isLineSdk(sdk)) throw new Refusal('upstream_changed', 'line module');
  // The credential manager keeps the session and the Letter Sealing keys in
  // the account's data directory, which Core creates 0700 outside backups.
  const credentials = new sdk.LineCredentialManager(options.configDir);
  let client: Client | null = null;
  let self = '';
  let started = false;
  let settled = false;
  let failure: unknown = null;
  let step: SignInStep | null = null;
  let wake = Promise.withResolvers<void>();

  // reset forgets a sign-in that did not finish, so the next call starts the
  // add-on's own QR flow again.
  const reset = () => {
    started = false;
    settled = false;
    failure = null;
    step = null;
    wake = Promise.withResolvers<void>();
  };

  const state = async (): Promise<SignInState> => {
    if (client) return 'signed_in';
    const account = await credentials.getAccount();
    if (!account?.auth_token) return 'needs_sign_in';
    const candidate = new sdk.LineClient(credentials);
    try {
      await candidate.login(account);
      self = (await candidate.getProfile()).mid;
      client = candidate;
      return 'signed_in';
    } catch (error) {
      if (revoked(error)) return 'needs_sign_in';
      throw error;
    }
  };

  const session = async (): Promise<Client> => {
    if ((await state()) !== 'signed_in' || !client) throw new Refusal('needs_sign_in', 'LINE sign-in is required');
    return client;
  };

  const start = () => {
    started = true;
    const candidate = new sdk.LineClient(credentials);
    let login: Promise<LoginResult>;
    try {
      login = candidate.loginWithQR({
        onQRUrl: url => {
          if (!url || settled) return;
          step = { state: 'needs_sign_in', show: { kind: 'url', value: url } };
          wake.resolve();
        },
        onPincode: pin => {
          if (!pin || settled) return;
          step = { state: 'needs_sign_in', show: { kind: 'code', value: pin } };
          wake.resolve();
        },
      });
    } catch (error) {
      failure = error;
      settled = true;
      wake.resolve();
      return;
    }
    void login
      .then(async result => {
        if (!result?.authenticated) throw new Refusal('sign_in_failed', 'LINE did not finish sign-in');
        self = (await candidate.getProfile()).mid;
        client = candidate;
        step = { state: 'signed_in' };
      })
      .catch(error => {
        failure = error;
      })
      .finally(() => {
        settled = true;
        wake.resolve();
      });
  };

  const begin = async (): Promise<SignInStep> => {
    if ((await state()) === 'signed_in') return { state: 'signed_in' };
    if (settled) reset();
    if (!started) start();
    if (!settled) await wake.promise;
    // A QR code or PIN already shown is returned again so a reloaded screen
    // shows it; only a finished sign-in or a refusal changes the answer.
    if (!settled) return step ?? { state: 'needs_sign_in' };
    if (failure) {
      if (revoked(failure)) return { state: 'needs_sign_in', prompt: { key: 'retry', kind: 'text', text: 'Sign in again' } };
      throw failure;
    }
    return step ?? { state: 'signed_in' };
  };

  const capabilities: Capabilities = cap({
    historyDepth: 'newest-N',
    cursorKind: 'none',
    edits: false,
    deletions: false,
    liveEvents: true,
    intervalSeconds: INTERVAL_SECONDS,
    readWindowSeconds: 0,
  });

  const record = (value: LineMessage): Message => {
    if (!value || typeof value !== 'object' || typeof value.message_id !== 'string' || value.message_id === '') {
      throw new Refusal('upstream_changed', 'LINE message id');
    }
    const at = isoTime(value.sent_at);
    if (!at) throw new Refusal('upstream_changed', 'LINE message time');
    const failed = value.decryption_error !== undefined;
    const authorId = typeof value.author_id === 'string' ? value.author_id : '';
    const message: Message = {
      id: value.message_id,
      author: authorId ? { id: authorId, name: typeof value.author_name === 'string' ? value.author_name : '' } : { id: 'line:system', name: 'LINE' },
      route: authorId ? (authorId === self ? 'person' : 'external_participant') : 'system',
      at,
      // A decryption failure keeps the record but never the ciphertext as text.
      text: failed ? '' : typeof value.text === 'string' ? value.text : '',
    };
    if (failed) message.decryptionFailed = true;
    return message;
  };

  const liveRecord = (value: RawMessage): Message => {
    const raw = value?.raw?.id;
    const id = typeof raw === 'string' || typeof raw === 'number' ? String(raw) : '';
    const at = isoTime(value?.raw?.createdTime);
    if (!id || !at) throw new Refusal('upstream_changed', 'LINE live message');
    const failed = value.decryption_error !== undefined;
    const authorId = typeof value.from?.id === 'string' ? value.from.id : '';
    const message: Message = {
      id,
      author: authorId ? { id: authorId, name: '' } : { id: 'line:system', name: 'LINE' },
      route: authorId ? (authorId === self ? 'person' : 'external_participant') : 'system',
      at,
      text: failed ? '' : typeof value.text === 'string' ? value.text : '',
    };
    if (failed) message.decryptionFailed = true;
    return message;
  };

  return {
    platform: 'line',
    capabilities(): Capabilities {
      return capabilities;
    },
    signInState: state,
    beginSignIn: begin,
    // LINE asks the person to type the PIN into the phone; there is no prompt
    // for this add-on to answer.
    async answer(): Promise<SignInStep> {
      return (await state()) === 'signed_in' ? { state: 'signed_in' } : step ?? { state: 'needs_sign_in' };
    },
    async listChats(): Promise<Chat[]> {
      const open = await session();
      const chats = await open.getChats({ limit: LIMITS.chats });
      if (!Array.isArray(chats)) throw new Refusal('upstream_changed', 'LINE chats');
      return clamp(
        chats.map(chat => ({
          id: chat.chat_id,
          name: typeof chat.display_name === 'string' && chat.display_name !== '' ? chat.display_name : chat.chat_id,
          kind: kindOf(chat.type),
        })),
        LIMITS.chats,
      );
    },
    async page(chat: string): Promise<Page> {
      const open = await session();
      const newest = await open.getMessages(chat, { count: NEWEST });
      if (!Array.isArray(newest)) throw new Refusal('upstream_changed', 'LINE messages');
      const messages = clamp(newest.map(record).sort(order), LIMITS.page);
      const through = messages.length > 0 ? messages[messages.length - 1].at : undefined;
      // No cursor survives the restart: the next read asks for the newest
      // messages again and Core drops the ones it already stored.
      return {
        messages,
        deletions: [],
        next: '',
        hasMore: false,
        ...(through ? { through } : {}),
        coverage: { historyDepth: capabilities.historyDepth, cursorSurvivesRestart: capabilities.cursorKind !== 'none' },
      };
    },
    async members(): Promise<Author[]> {
      // The pinned read methods expose no group member list. Members stay
      // unknown rather than being guessed from message authors.
      return [];
    },
    async whoami(): Promise<Author> {
      const profile = await (await session()).getProfile();
      if (!profile?.mid) throw new Refusal('upstream_changed', 'LINE profile');
      return { id: profile.mid, name: typeof profile.display_name === 'string' ? profile.display_name : '' };
    },
    live(deliver: (event: LiveEvent) => void): () => void {
      const stop = new AbortController();
      void (async () => {
        try {
          const open = await session();
          for await (const event of open.streamEvents(stop.signal)) {
            if (stop.signal.aborted) return;
            if (event?.kind === 'message') deliver({ event: 'message', message: liveRecord(event.message) });
          }
        } catch {
          // A live stream that ends is retried by the next sync; the source's
          // registry row reports a failure meanwhile.
        }
      })();
      return () => stop.abort();
    },
  };
}

export type { DriverOptions };
