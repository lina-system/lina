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
      calls.push('getAccount');
      return accounts[id ?? current ?? ''] ?? null;
    },
    async setAccount(account: RecordedAccount) {
      calls.push('setAccount');
      accounts[account.account_id] = account;
      persist();
    },
    async setCurrentAccount(id: string) {
      calls.push('setCurrentAccount');
      current = id;
      persist();
    },
    async loadPendingLogin() {
      calls.push('loadPendingLogin');
      return pendingLogin;
    },
    async savePendingLogin(state: { device_uuid: string; device_type: string; email: string }) {
      calls.push('savePendingLogin');
      pendingLogin = state;
      persist();
    },
    async clearPendingLogin() {
      calls.push('clearPendingLogin');
      pendingLogin = null;
      persist();
    },
  };

  class KakaoTalkClient {
    async login(input: { oauthToken: string; userId: string; deviceUuid?: string; deviceType?: string }) {
      calls.push('login');
      if (script.expire) {
        throw Object.assign(new Error('stored session expired'), { code: 'invalid_access_token' });
      }
      if (!input.oauthToken || !input.userId) throw Object.assign(new Error('missing token'), { code: 'login_rejected' });
      own = String(input.userId);
      return this;
    }
    async acquireSession() {
      calls.push('acquireSession');
      return {};
    }
    async getChats() {
      calls.push('getChats');
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
      calls.push('getMessagePage');
      const from = options?.from ?? '';
      return script.pages?.[from] ?? { messages: [], next_cursor: null, complete: true };
    }
    async getMembers() {
      calls.push('getMembers');
      return script.members ?? [];
    }
    async getProfile() {
      calls.push('getProfile');
      return script.profile ?? { user_id: own, nickname: '' };
    }
    onPush(handler: (packet: { method: string; body: Record<string, unknown> }) => void) {
      calls.push('onPush');
      sink = handler;
      return () => {
        calls.push('offPush');
        sink = null;
      };
    }
    close() {
      calls.push('close');
    }
  }

  class KakaoCredentialManager {
    constructor() {
      calls.push('KakaoCredentialManager');
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
      calls.push('attemptLogin');
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
      calls.push('requestPasscode');
      return { authenticated: false, next_action: 'confirm_on_phone', passcode: script.login?.passcode ?? '0000' };
    },
    async registerDevice() {
      calls.push('registerDevice');
      const delay = script.login?.registration?.delayMs ?? 0;
      if (delay > 0) await new Promise(resolve => setTimeout(resolve, delay));
      return script.login?.registration?.error
        ? { authenticated: false, error: script.login.registration.error }
        : { authenticated: false };
    },
    generateDeviceUuid() {
      calls.push('generateDeviceUuid');
      return 'fixture-device-uuid';
    },
    classifyKakaoChat(chat) {
      calls.push('classifyKakaoChat');
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
