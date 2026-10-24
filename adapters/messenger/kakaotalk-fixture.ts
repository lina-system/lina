// SPDX-License-Identifier: Apache-2.0

// A recorded, LOCO-shaped KakaoTalk for the add-on's tests and Core's headless
// end-to-end run. It answers from a recorded script in the add-on's data
// directory (`script.json`) and contacts nothing: no LOCO socket, no HTTP, no
// session. Every SDK call it serves is recorded, so a test can prove the driver
// reached nothing outside the read allowlist.
//
// The script mirrors the records the pinned SDK hands back:
//
//	{
//	  "accounts": { "<account id>": <credentials> },  // stored session
//	  "expire": true,                                  // stored token rejected
//	  "session": { "error": "..." },                    // live session refused
//	  "login": { "attempts": [<login result>], "passcode": "4821",
//             "registration": { "delayMs": 0, "error": null } },
//	  "profile": { "user_id": "...", "nickname": "..." },
//	  "chats": [ { "chat_id", "type", "display_name", "title", "active_members", "last_log_id" } ],
//	  "pages": { "<cursor>": { "messages": [<log>], "next_cursor": "...", "complete": true } },
//	  "members": [ { "user_id", "nickname" } ],
//	  "push": [ <MSG packet> ]
//	}
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { KakaoSDK } from './src/kakaotalk.ts';

export type Recorded = {
  accounts?: Record<string, RecordedAccount>;
  expire?: boolean;
  // session: an injected failure of acquireSession, for the live path.
  session?: { error?: string };
  login?: { attempts?: RecordedLogin[]; passcode?: string; registration?: { error?: string; delayMs?: number } };
  profile?: { user_id: string; nickname: string };
  chats?: RecordedChat[];
  pages?: Record<string, RecordedPage>;
  members?: RecordedMember[];
  push?: Array<{ method: string; body: Record<string, unknown> }>;
};

type RecordedAccount = {
  account_id: string;
  oauth_token: string;
  user_id: string;
  device_uuid: string;
  device_type: string;
};

type RecordedLogin = {
  authenticated: boolean;
  next_action?: string;
  error?: string;
  account_id?: string;
  user_id?: string;
};

type RecordedChat = {
  chat_id: string;
  type: number | string;
  display_name: string | null;
  title: string | null;
  active_members: number;
  last_log_id?: string;
};

type RecordedLog = {
  log_id: string;
  type: number;
  author_id: number | string;
  author_name: string | null;
  message: string;
  attachment: Record<string, unknown> | null;
  sent_at: number;
};

type RecordedPage = { messages: RecordedLog[]; next_cursor: string | null; complete: boolean };

type RecordedMember = { user_id: string; nickname: string };

export type Fixture = {
  sdk: KakaoSDK;
  calls: string[];
  // deliver sends a recorded push to the current subscriber.
  deliver(packet: { method: string; body: Record<string, unknown> }): void;
};

export function fixtureSDK(script: Recorded, configDir: string): Fixture {
  const calls: string[] = [];
  const record = (method: string) => {
    calls.push(method);
    writeFileSync(join(configDir, 'calls.json'), JSON.stringify(calls), { mode: 0o600 });
  };
  let sink: ((packet: { method: string; body: Record<string, unknown> }) => void) | null = null;
  const accounts: Record<string, RecordedAccount> = { ...(script.accounts ?? {}) };
  let current: string | null = Object.keys(accounts)[0] ?? null;
  let pendingLogin: { device_uuid: string; device_type: string; email: string } | null = null;
  const attempts = [...(script.login?.attempts ?? [])];
  let own = '';

  const persist = () => {
    writeFileSync(join(configDir, 'fixture-credentials.json'), JSON.stringify({ accounts, current, pendingLogin }), { mode: 0o600 });
  };
  const restore = () => {
    try {
      const saved = JSON.parse(readFileSync(join(configDir, 'fixture-credentials.json'), 'utf8'));
      for (const [id, value] of Object.entries(saved.accounts ?? {})) accounts[id] = value as RecordedAccount;
      current = saved.current ?? current;
      pendingLogin = saved.pendingLogin ?? null;
    } catch {
      // No stored session yet.
    }
  };
  restore();

  const credentials = {
    async getAccount(id?: string) {
      record('getAccount');
      return accounts[id ?? current ?? ''] ?? null;
    },
    async setAccount(account: RecordedAccount) {
      record('setAccount');
      accounts[account.account_id] = account;
      persist();
    },
    async setCurrentAccount(id: string) {
      record('setCurrentAccount');
      current = id;
      persist();
    },
    async loadPendingLogin() {
      record('loadPendingLogin');
      return pendingLogin;
    },
    async savePendingLogin(state: { device_uuid: string; device_type: string; email: string }) {
      record('savePendingLogin');
      pendingLogin = state;
      persist();
    },
    async clearPendingLogin() {
      record('clearPendingLogin');
      pendingLogin = null;
      persist();
    },
  };

  class KakaoTalkClient {
    private requireLogin() {
      if (!own) throw new Error('client has not logged in');
    }
    async login(input: { oauthToken: string; userId: string; deviceUuid?: string; deviceType?: string }) {
      record('login');
      if (script.expire) {
        throw Object.assign(new Error('stored session expired'), { code: 'invalid_access_token' });
      }
      if (!input.oauthToken || !input.userId) throw Object.assign(new Error('missing token'), { code: 'login_rejected' });
      own = String(input.userId);
      return this;
    }
    async acquireSession() {
      this.requireLogin();
      record('acquireSession');
      if (script.session?.error) throw new Error(script.session.error);
      for (const packet of script.push ?? []) sink?.(packet);
      return {};
    }
    async getChats() {
      this.requireLogin();
      record('getChats');
      return (script.chats ?? []).map(chat => ({
        chat_id: chat.chat_id,
        type: chat.type,
        display_name: chat.display_name,
        title: chat.title,
        active_members: chat.active_members,
        unread_count: 0,
        last_message: chat.last_log_id
          ? { author_id: 0, author_name: null, message: '', sent_at: 0, log_id: chat.last_log_id }
          : null,
      }));
    }
    async getMessagePage(_chat: string, options?: { count?: number; from?: string }) {
      this.requireLogin();
      record('getMessagePage');
      const from = options?.from ?? '';
      return script.pages?.[from] ?? { messages: [], next_cursor: null, complete: true };
    }
    async getMembers() {
      this.requireLogin();
      record('getMembers');
      return script.members ?? [];
    }
    async getProfile() {
      this.requireLogin();
      record('getProfile');
      return script.profile ?? { user_id: own, nickname: '' };
    }
    onPush(handler: (packet: { method: string; body: Record<string, unknown> }) => void) {
      record('onPush');
      sink = handler;
      return () => {
        record('offPush');
        sink = null;
      };
    }
    close() {
      record('close');
    }
  }

  class KakaoCredentialManager {
    constructor() {
      record('KakaoCredentialManager');
    }
    getAccount = credentials.getAccount;
    setAccount = credentials.setAccount;
    setCurrentAccount = credentials.setCurrentAccount;
    loadPendingLogin = credentials.loadPendingLogin;
    savePendingLogin = credentials.savePendingLogin;
    clearPendingLogin = credentials.clearPendingLogin;
  }

  const next = () => attempts.shift() ?? { authenticated: false, error: 'login_failed' };

  const sdk: KakaoSDK = {
    KakaoTalkClient,
    KakaoCredentialManager,
    async attemptLogin(_email, _password, deviceUuid, deviceType, forced) {
      record('attemptLogin');
      if (forced) throw new Error('the tablet slot is never forced');
      const result = next();
      if (result.authenticated) {
        return {
          ...result,
          credentials: {
            access_token: 'fixture-access-token',
            user_id: String(result.user_id ?? script.profile?.user_id ?? ''),
            device_uuid: deviceUuid,
            device_type: deviceType,
          },
        };
      }
      return result;
    },
    async requestPasscode() {
      record('requestPasscode');
      return { authenticated: false, next_action: 'confirm_on_phone', passcode: script.login?.passcode ?? '0000' };
    },
    async registerDevice() {
      record('registerDevice');
      const delay = script.login?.registration?.delayMs ?? 0;
      if (delay > 0) await new Promise(resolve => setTimeout(resolve, delay));
      return script.login?.registration?.error
        ? { authenticated: false, error: script.login.registration.error }
        : { authenticated: false };
    },
    generateDeviceUuid() {
      record('generateDeviceUuid');
      return 'fixture-device-uuid';
    },
    classifyKakaoChat(chat) {
      record('classifyKakaoChat');
      const open = new Set([2, 13, 14, 15, 16, 'OM', 'OD']);
      if (open.has(chat.type)) return 'open';
      if (typeof chat.type === 'string' && !['DirectChat', 'MultiChat', 'PlusChat', 'MemoChat', 'OM', 'OD'].includes(chat.type)) return 'unknown';
      return chat.active_members <= 2 ? 'dm' : 'group';
    },
  };

  return {
    sdk,
    calls,
    deliver(packet) {
      if (sink) sink(packet);
    },
  };
}

export function recordedFrom(configDir: string): Recorded {
  try {
    return JSON.parse(readFileSync(join(configDir, 'script.json'), 'utf8')) as Recorded;
  } catch {
    return {};
  }
}
