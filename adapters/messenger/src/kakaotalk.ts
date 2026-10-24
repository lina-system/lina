// SPDX-License-Identifier: Apache-2.0

// The KakaoTalk driver. It maps this add-on's read operations onto the pinned
// agent-messenger KakaoTalk client, signing in as a tablet sub-device through
// the SDK's own passcode flow. KakaoTalk has no public protocol: the SDK speaks
// LOCO, the reverse-engineered protocol of the official app. The person accepts
// that risk on the connect card before this driver runs.
//
// Reads only: the operation table in protocol.ts has no write route, and this
// driver names only read and sign-in methods (src/allowlist.ts). A read never
// enters the room, so it never advances a read watermark.
//
// Seen in agent-messenger 2.39.1: chat list via `getChats`, forward-only pages
// from a `log_id` via `getMessagePage`, members via `getMembers`, the account's
// profile via `getProfile`, live `MSG` pushes via `onPush`, and the tablet
// sign-in steps `attemptLogin`, `requestPasscode` and `registerDevice`.
// Inferred: an edit or a deletion is not observable — upstream states that the
// platform offers neither — so this driver reports each message as first read.
// Unknown: server behavior for an account, region or client version.
import { Refusal, type Attachment, type Author, type Capabilities, type Chat, type Driver, type LiveEvent, type Message, type Page, type Route, type SignInState, type SignInStep } from './protocol.ts';
import { cap, type DriverOptions } from './driver.ts';

// The pinned SDK surface this driver uses. It is a narrow view of
// `agent-messenger/kakaotalk`; the contract test proves nothing else is called.
export type KakaoSDK = {
  KakaoTalkClient: new () => KakaoClient;
  KakaoCredentialManager: new (configDir?: string) => KakaoCredentials;
  attemptLogin(email: string, password: string, deviceUuid: string, deviceType: string, forced: boolean): Promise<KakaoLogin>;
  requestPasscode(email: string, password: string, deviceUuid: string): Promise<KakaoLogin>;
  registerDevice(email: string, password: string, passcode: string, deviceUuid: string): Promise<KakaoLogin>;
  generateDeviceUuid(): string;
  classifyKakaoChat(chat: { type: number | string; active_members: number }): 'dm' | 'group' | 'open' | 'unknown';
};

type KakaoCredentials = {
  getAccount(id?: string): Promise<KakaoAccount | null>;
  setAccount(account: KakaoAccount): Promise<void>;
  setCurrentAccount(id: string): Promise<void>;
  loadPendingLogin(): Promise<{ device_uuid: string; device_type: string; email: string } | null>;
  savePendingLogin(state: { device_uuid: string; device_type: string; email: string; created_at: string }): Promise<void>;
  clearPendingLogin(): Promise<void>;
};

type KakaoAccount = {
  account_id: string;
  oauth_token: string;
  user_id: string;
  device_uuid: string;
  device_type: string;
};

type KakaoChatRecord = {
  chat_id: string;
  type: number | string;
  display_name: string | null;
  title: string | null;
  active_members: number;
  last_message: { log_id: string; sent_at: number } | null;
};

type KakaoLogRecord = {
  log_id: string;
  type: number;
  author_id: number | string;
  author_name: string | null;
  message: string;
  attachment: Record<string, unknown> | null;
  sent_at: number;
};

type KakaoPage = { messages: KakaoLogRecord[]; next_cursor: string | null; complete: boolean };

type KakaoMember = { user_id: string; nickname: string };

type KakaoProfile = { user_id: string; nickname: string };

type KakaoLogin = {
  authenticated: boolean;
  next_action?: string;
  error?: string;
  passcode?: string;
  account_id?: string;
  user_id?: string;
  credentials?: {
    access_token: string;
    user_id: string;
    device_uuid: string;
    device_type: string;
  };
};

type KakaoClient = {
  login(credentials: { oauthToken: string; userId: string; deviceUuid?: string; deviceType?: string }, accountId?: string): Promise<KakaoClient>;
  acquireSession(): Promise<unknown>;
  getChats(options?: { all?: boolean; search?: string; resolveTitles?: boolean }): Promise<KakaoChatRecord[]>;
  getMessagePage(chatId: string, options?: { count?: number; from?: string }): Promise<KakaoPage>;
  getMembers(chatId: string): Promise<KakaoMember[]>;
  getProfile(): Promise<KakaoProfile>;
  onPush(handler: (packet: { method: string; body: Record<string, unknown> }) => void): () => void;
  close(): void;
};

// Message types the pinned SDK reports. Anything else keeps its text and
// attachment as the platform sent them.
const TYPE = { PHOTO: 2, VIDEO: 3, AUDIO: 5, FILE: 18, REPLY: 26, MULTIPHOTO: 27 } as const;

// A stored session the platform no longer accepts: the source needs sign-in and
// the person signs in again, while every other source keeps running.
const NEEDS_SIGN_IN: Record<string, true> = { invalid_access_token: true, login_rejected: true };

export function openKakaoTalk(sdk: KakaoSDK, options: DriverOptions): Driver {
  const credentials = new sdk.KakaoCredentialManager(options.configDir);
  const client = new sdk.KakaoTalkClient();

  let account: KakaoAccount | null = null;
  let connecting: Promise<void> | null = null;
  let signedIn = false;
  let ownId = '';
  let failure = '';
  let pending: { email: string; password: string; deviceUuid: string } | null = null;
  // shown is the passcode Kakao displays while the tablet registration runs. It
  // stays in memory until the attempt ends so a repeated step can show it again.
  let shown = '';
  let confirming = false;
  let registering: Promise<void> | null = null;

  const connect = async (): Promise<void> => {
    if (signedIn) return;
    if (failure) throw new Refusal('source_failing', failure);
    account = account ?? (await credentials.getAccount());
    if (!account) return;
    try {
      await client.login({
        oauthToken: account.oauth_token,
        userId: account.user_id,
        deviceUuid: account.device_uuid,
        deviceType: 'tablet',
      }, account.account_id);
      ownId = String(account.user_id);
      signedIn = true;
    } catch (error) {
      const code = error instanceof Error && Object.hasOwn(error, 'code') ? String(Reflect.get(error, 'code')) : '';
      if (NEEDS_SIGN_IN[code]) {
        failure = 'needs_sign_in';
        throw new Refusal('needs_sign_in', failure);
      }
      failure = code === '' ? 'sign-in failed' : 'sign-in failed: ' + code;
      throw new Refusal('source_failing', failure);
    }
  };

  // ready joins one login attempt, so a page read and a live subscription that
  // start together do not open two sessions for one account.
  const ready = async (): Promise<void> => {
    if (signedIn) return;
    if (!connecting) {
      connecting = connect().finally(() => {
        connecting = null;
      });
    }
    await connecting;
  };

  const requireClient = async (): Promise<KakaoClient> => {
    await ready();
    if (!signedIn) throw new Refusal('needs_sign_in', 'not signed in');
    return client;
  };

  const keep = async (login: KakaoLogin): Promise<void> => {
    if (!login.credentials?.access_token) {
      failure = 'login failed';
      throw new Refusal('source_failing', failure);
    }
    const saved: KakaoAccount = {
      account_id: String(login.account_id ?? login.user_id ?? login.credentials.user_id),
      oauth_token: login.credentials.access_token,
      user_id: login.credentials.user_id,
      device_uuid: login.credentials.device_uuid,
      device_type: 'tablet',
    };
    await credentials.setAccount(saved);
    await credentials.setCurrentAccount(saved.account_id);
    await credentials.clearPendingLogin();
    account = saved;
    ownId = String(saved.user_id);
    signedIn = true;
    failure = '';
    pending = null;
    shown = '';
  };

  // submit tries the tablet slot once. The slot is never forced, so no other
  // device of the person is evicted; an occupied slot asks for the passcode.
  const submit = async (): Promise<SignInStep> => {
    if (!pending) throw new Refusal('invalid_request', 'no sign-in in progress');
    const result = await sdk.attemptLogin(pending.email, pending.password, pending.deviceUuid, 'tablet', false);
    if (result.authenticated) {
      await keep(result);
      return { state: 'signed_in' };
    }
    if (result.next_action === 'provide_passcode') {
      const code = await sdk.requestPasscode(pending.email, pending.password, pending.deviceUuid);
      if (!code.passcode) {
        failure = code.error ?? 'passcode request failed';
        pending = null;
        return { state: 'failing' };
      }
      await credentials.savePendingLogin({
        device_uuid: pending.deviceUuid,
        device_type: 'tablet',
        email: pending.email,
        created_at: (options.now ? options.now() : new Date()).toISOString(),
      });
      // The code is shown now and the person enters it on their phone. The
      // registration then finishes in the background; nothing of it is kept.
      shown = code.passcode;
      confirming = true;
      registering = confirm().finally(() => {
        confirming = false;
        registering = null;
      });
      return { state: 'needs_sign_in', show: { kind: 'code', value: code.passcode } };
    }
    failure = result.error ?? 'login failed';
    pending = null;
    return { state: 'failing' };
  };

  const confirm = async (): Promise<void> => {
    if (!pending) return;
    const registration = await sdk.registerDevice(pending.email, pending.password, '', pending.deviceUuid);
    if (registration.error) {
      failure = registration.error;
      pending = null;
      return;
    }
    const result = await sdk.attemptLogin(pending.email, pending.password, pending.deviceUuid, 'tablet', false);
    if (!result.authenticated) {
      failure = result.error ?? 'login failed';
      pending = null;
      return;
    }
    await keep(result);
  };

  const signInState = async (): Promise<SignInState> => {
    if (signedIn) return 'signed_in';
    if (failure) return failure === 'needs_sign_in' ? 'needs_sign_in' : 'failing';
    if (pending || confirming) return 'needs_sign_in';
    try {
      await ready();
    } catch {
      // ready records the cause; the state below reports it.
    }
    if (signedIn) return 'signed_in';
    if (failure !== '') return failure === 'needs_sign_in' ? 'needs_sign_in' : 'failing';
    return 'needs_sign_in';
  };

  const message = (log: KakaoLogRecord): Message => {
    const id = typeof log.log_id === 'string' ? log.log_id : String(log.log_id ?? '');
    if (id === '') throw new Refusal('upstream_changed', 'log id');
    const sent = typeof log.sent_at === 'number' && Number.isFinite(log.sent_at) ? new Date(log.sent_at) : null;
    if (!sent) throw new Refusal('upstream_changed', 'sent at');
    const authorId = String(log.author_id);
    const route: Route = ownId !== '' && authorId === ownId ? 'person'
      : authorId === '0' ? 'system' : 'external_participant';
    const result: Message = {
      id,
      author: { id: authorId, name: log.author_name ?? '' },
      route,
      at: sent.toISOString(),
      text: typeof log.message === 'string' ? log.message : '',
    };
    if (log.type === TYPE.REPLY) {
      const source = log.attachment?.src_logId;
      if (typeof source === 'string' && source !== '') result.replyTo = source;
    }
    const files = attachments(log);
    if (files.length > 0) result.attachments = files;
    return result;
  };

  return {
    platform: 'kakaotalk',

    capabilities(): Capabilities {
      return cap({
        historyDepth: 'platform-window',
        cursorKind: 'log-id',
        edits: false,
        deletions: false,
        liveEvents: true,
        intervalSeconds: 900,
        readWindowSeconds: 172_800,
      });
    },

    signInState,

    async beginSignIn(): Promise<SignInStep> {
      try {
        await ready();
      } catch {
        // A stored session the platform rejected asks for sign-in again.
      }
      if (signedIn) return { state: 'signed_in' };
      if (failure !== '' && failure !== 'needs_sign_in') return { state: 'failing' };
      if (confirming) {
        // The phone confirmation is still in flight; show the same code.
        return { state: 'needs_sign_in', show: { kind: 'code', value: shown } };
      }
      if (pending) {
        return pending.email === ''
          ? { state: 'needs_sign_in', prompt: { key: 'email', kind: 'text', text: 'KakaoTalk email' } }
          : { state: 'needs_sign_in', prompt: { key: 'password', kind: 'secret', text: 'KakaoTalk password' } };
      }
      const saved = await credentials.loadPendingLogin();
      pending = { email: saved?.email ?? '', password: '', deviceUuid: saved?.device_uuid ?? sdk.generateDeviceUuid() };
      return pending.email === ''
        ? { state: 'needs_sign_in', prompt: { key: 'email', kind: 'text', text: 'KakaoTalk email' } }
        : { state: 'needs_sign_in', prompt: { key: 'password', kind: 'secret', text: 'KakaoTalk password' } };
    },

    async answer(key: string, value: string): Promise<SignInStep> {
      if (confirming) throw new Refusal('invalid_request', 'sign-in in progress');
      if (!pending) throw new Refusal('invalid_request', 'no sign-in in progress');
      if (key === 'email') {
        if (!value.includes('@')) throw new Refusal('invalid_request', 'email');
        pending.email = value;
        return { state: 'needs_sign_in', prompt: { key: 'password', kind: 'secret', text: 'KakaoTalk password' } };
      }
      if (key === 'password') {
        if (value === '') throw new Refusal('invalid_request', 'password');
        pending.password = value;
        return await submit();
      }
      throw new Refusal('invalid_request', 'prompt');
    },

    async listChats(): Promise<Chat[]> {
      const live = await requireClient();
      const chats = await live.getChats();
      const out: Chat[] = [];
      for (const chat of chats) {
        const id = typeof chat.chat_id === 'string' ? chat.chat_id : String(chat.chat_id ?? '');
        if (id === '') throw new Refusal('upstream_changed', 'chat id');
        // An unknown chat class is protocol drift: the source fails instead of
        // guessing whether the room is a direct chat or a group.
        switch (sdk.classifyKakaoChat({ type: chat.type, active_members: chat.active_members })) {
          case 'dm':
            out.push({ id, name: chat.display_name ?? chat.title ?? '', kind: 'direct', lastMessageId: chat.last_message?.log_id });
            break;
          case 'group':
          case 'open':
            out.push({ id, name: chat.display_name ?? chat.title ?? '', kind: 'group', lastMessageId: chat.last_message?.log_id });
            break;
          default:
            throw new Refusal('upstream_changed', 'chat kind');
        }
      }
      return out;
    },

    async page(chat: string, cursor: string): Promise<Page> {
      const live = await requireClient();
      const page = await live.getMessagePage(chat, { count: 100, from: cursor === '' ? undefined : cursor });
      const messages = page.messages.map(message);
      const last = messages.at(-1);
      return {
        messages,
        deletions: [],
        next: page.next_cursor ?? (last ? last.id : cursor),
        hasMore: !page.complete,
        through: page.complete && last ? last.at : undefined,
      };
    },

    async members(chat: string): Promise<Author[]> {
      const live = await requireClient();
      const members = await live.getMembers(chat);
      return members.map(member => ({ id: String(member.user_id), name: member.nickname ?? '' }));
    },

    async whoami(): Promise<Author> {
      const live = await requireClient();
      const profile = await live.getProfile();
      return { id: String(profile.user_id), name: profile.nickname ?? '' };
    },

    live(deliver: (event: LiveEvent) => void): () => void {
      void (async () => {
        try {
          const live = await requireClient();
          await live.acquireSession();
        } catch {
          // The source's sign-in state reports the cause; no push arrives.
        }
      })();
      return client.onPush(packet => {
        if (packet.method !== 'MSG') return;
        const raw = packet.body.chatLog;
        if (!raw || typeof raw !== 'object') return;
        const log = raw as Record<string, unknown>;
        const attachment = log.attachment && typeof log.attachment === 'object' && !Array.isArray(log.attachment)
          ? log.attachment as Record<string, unknown>
          : null;
        const chat = String(packet.body.chatId ?? log.chatId ?? '');
        if (chat === '') return;
        const author = log.authorId;
        deliver({
          event: 'message',
          chat,
          message: message({
            log_id: String(log.logId ?? ''),
            type: typeof log.type === 'number' ? log.type : 0,
            author_id: typeof author === 'number' || typeof author === 'string' ? author : String(author ?? 0),
            author_name: typeof log.authorName === 'string' ? log.authorName : null,
            message: typeof log.message === 'string' ? log.message : '',
            attachment,
            sent_at: typeof log.sendAt === 'number' ? log.sendAt : 0,
          }),
        });
      });
    },
  };
}

// attachments reads the platform's metadata for a message that carries a file.
// Bytes are never fetched here; Core asks for one only when the person does.
function attachments(log: KakaoLogRecord): Attachment[] {
  const extra = log.attachment;
  if (!extra) return [];
  const text = (value: unknown): string => (typeof value === 'string' ? value : '');
  const size = (value: unknown): number => (typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0);
  if (log.type === TYPE.MULTIPHOTO && Array.isArray(extra.kl)) {
    const mimes = Array.isArray(extra.mtl) ? extra.mtl : [];
    const sizes = Array.isArray(extra.sl) ? extra.sl : [];
    const names = Array.isArray(extra.name) ? extra.name : [];
    return extra.kl.flatMap((key, index) => typeof key === 'string' && key !== ''
      ? [{ id: key, name: text(names[index]), mimeType: text(mimes[index]) || 'image/jpeg', size: size(sizes[index]) }]
      : []);
  }
  const id = text(extra.k) || text(extra.path) || text(extra.name);
  if (id === '') return [];
  return [{ id, name: text(extra.name), mimeType: text(extra.mt), size: size(extra.s) }];
}
