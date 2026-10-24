// SPDX-License-Identifier: Apache-2.0

// A recorded Discord SDK for the add-on's tests and Core's end-to-end run. It
// answers the same surface the pinned SDK's Discord slice does, from a script in
// the add-on's data directory (`discord.json`), and records every entry point it
// is asked for. No token is real and nothing contacts Discord.
//
//	{ "token": "...", "revoked": false, "qr": "https://discord.com/ra/x",
//	  "me": {...}, "servers": [...], "channels": { "<guild>": [...] },
//	  "dms": [...], "messages": { "<chat>": [...] }, "users": { "<guild>": [...] } }
//
// The script is read again on every call, so a test can rewrite it to make an
// edit or a new message arrive on the next sync. A token the fixture's own
// credential manager stores stays in this process, as the add-on's data
// directory would hold it.
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

type User = { id: string; username: string; global_name?: string };
type Channel = { id: string; guild_id: string; name: string; type: number };
type DM = { id: string; type: number; name?: string; recipients: User[] };
type Message = { id: string; channel_id: string; author: { id: string; username: string }; content: string; timestamp: string; edited_timestamp?: string };

export type Script = {
  token?: string | null;
  revoked?: boolean;
  qr?: string;
  me?: User;
  servers?: { id: string; name: string }[];
  channels?: Record<string, Channel[]>;
  dms?: DM[];
  messages?: Record<string, Message[]>;
  users?: Record<string, User[]>;
};

function read(configDir: string): Script {
  const path = join(configDir, 'discord.json');
  if (!existsSync(path)) return {};
  try {
    return JSON.parse(readFileSync(path, 'utf8')) as Script;
  } catch {
    return {};
  }
}

const unauthorized = (): Error => {
  const error = new Error('401: Unauthorized') as Error & { code: string };
  error.code = 'http_401';
  return error;
};

export function discordFixture(configDir: string) {
  const calls: string[] = [];
  let stored: string | null = null;
  const script = (): Script => {
    const recorded = read(configDir);
    return stored === null ? recorded : { ...recorded, token: stored };
  };
  class DiscordCredentialManager {
    constructor(_dir: string) {
      calls.push('DiscordCredentialManager');
    }
    async getToken(): Promise<string | null> {
      calls.push('getToken');
      return script().token ?? null;
    }
    async setToken(token: string): Promise<void> {
      calls.push('setToken');
      stored = token;
    }
  }
  class DiscordClient {
    constructor() {
      calls.push('DiscordClient');
    }
    async login(credentials?: { token: string }): Promise<DiscordClient> {
      calls.push('login');
      const state = script();
      if (state.revoked || !credentials || credentials.token !== state.token) throw unauthorized();
      return this;
    }
    async testAuth(): Promise<User> {
      calls.push('testAuth');
      const state = script();
      if (state.revoked) throw unauthorized();
      return state.me ?? { id: '900', username: 'jun' };
    }
    async listServers() {
      calls.push('listServers');
      return script().servers ?? [];
    }
    async listChannels(guild: string): Promise<Channel[]> {
      calls.push('listChannels');
      return script().channels?.[guild] ?? [];
    }
    async listDMChannels(): Promise<DM[]> {
      calls.push('listDMChannels');
      return script().dms ?? [];
    }
    async getMessages(chat: string, limit: number, options?: { around?: string }): Promise<Message[]> {
      calls.push('getMessages');
      const ordered = [...(script().messages?.[chat] ?? [])].sort((left, right) =>
        BigInt(left.id) < BigInt(right.id) ? -1 : BigInt(left.id) > BigInt(right.id) ? 1 : 0);
      // Around a message the API returns a window with messages on both sides.
      const anchor = options?.around === undefined ? -1 : ordered.findIndex(entry => entry.id === options.around);
      const start = anchor < 0 ? Math.max(0, ordered.length - limit) : Math.max(0, anchor - Math.floor(limit / 2));
      return ordered.slice(start, start + limit);
    }
    async listUsers(guild: string): Promise<User[]> {
      calls.push('listUsers');
      return script().users?.[guild] ?? [];
    }
    sendMessage = async () => {
      calls.push('sendMessage');
      throw new Error('never');
    };
    markRead = async () => {
      calls.push('markRead');
      throw new Error('never');
    };
    sendTyping = async () => {
      calls.push('sendTyping');
      throw new Error('never');
    };
  }
  const sdk = {
    DiscordClient,
    DiscordCredentialManager,
    loginWithRemoteAuth: async (options?: { onQrUrl?: (url: string) => void }) => {
      calls.push('loginWithRemoteAuth');
      const state = script();
      options?.onQrUrl?.(state.qr ?? 'https://discord.com/ra/synthetic');
      return { token: state.token ?? 'qr-token', user: state.me ?? null };
    },
  };
  return { sdk, calls };
}
