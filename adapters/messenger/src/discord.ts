// SPDX-License-Identifier: Apache-2.0

// Discord's personal account, read through the pinned agent-messenger SDK.
// Guild channels, direct messages and group DMs are each listed once; history
// pages walk forward by message snowflake inside the window the SDK serves, and
// an edit arrives as the message's own edited time. The account signs in only by
// scanning a QR code with the Discord mobile app: the driver never calls the
// SDK's credential extraction and hands the SDK only the token the add-on's own
// data directory holds. Nothing here writes, marks read or shows typing.
import { Refusal } from './protocol.ts';
import type { Author, Chat, Driver, LiveEvent, Page, SignInState, SignInStep } from './protocol.ts';
import type { DriverOptions } from './driver.ts';
import { Once, cap, decodeCursor, encodeCursor, LIMITS } from './driver.ts';
import { page as checkedPage } from './shape.ts';

type SdkUser = { id: string; username: string; global_name?: string; bot?: boolean };
type SdkGuild = { id: string; name: string };
type SdkChannel = { id: string; guild_id: string; name: string; type: number };
type SdkDM = { id: string; type: number; name?: string; recipients: SdkUser[] };
type SdkMessage = {
  id: string;
  channel_id: string;
  author: { id: string; username: string };
  content: string;
  timestamp: string;
  edited_timestamp?: string;
};

type SdkClient = {
  login(credentials?: { token: string }): Promise<SdkClient>;
  testAuth(): Promise<SdkUser>;
  listServers(): Promise<SdkGuild[]>;
  listChannels(guild: string): Promise<SdkChannel[]>;
  listDMChannels(): Promise<SdkDM[]>;
  getMessages(chat: string, limit: number, options?: { around?: string }): Promise<SdkMessage[]>;
  listUsers(guild: string): Promise<SdkUser[]>;
};

type SdkCredentials = {
  getToken(): Promise<string | null>;
  setToken(token: string): Promise<void>;
};

type Sdk = {
  DiscordClient: new () => SdkClient;
  DiscordCredentialManager: new (configDir: string) => SdkCredentials;
  loginWithRemoteAuth(options: { onQrUrl?: (url: string) => void }): Promise<{ token: string }>;
};

// WINDOW is the largest message page the API serves in one request. Around a
// message it returns about half on each side, so a forward page yields at most
// NEWER_CAP messages before it must ask again.
const WINDOW = 100;
const NEWER_CAP = 50;
const INTERVAL = 300;
const READ_WINDOW = 86_400;
const QR_TIMEOUT_MS = 10_000;
const SNOWFLAKE = /^[1-9][0-9]{0,19}$/;

// Channel types with a message timeline the account can read here. Categories,
// threads (reached through their own endpoints), directories, forums and media
// posts carry none.
const READABLE = new Set([0, 2, 5, 13]);

const signedInStep: SignInStep = { state: 'signed_in' };

const refused = (what: string): Refusal => new Refusal('invalid_request', what);

// isAuthError reports the platform's unauthorised answer: a revoked or expired
// session token. Anything else stays a failure with its cause.
function isAuthError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false;
  const code = 'code' in error && typeof error.code === 'string' ? error.code : '';
  const text = 'message' in error && typeof error.message === 'string' ? error.message : '';
  return code === 'http_401' || code === '401' || /\b401\b/.test(text);
}

function wire(message: SdkMessage, own: string) {
  return {
    id: message.id,
    author: { id: message.author?.id ?? '', name: message.author?.username ?? '' },
    // The platform states the author id, never a display name: the connected
    // account's own messages are the person's, everyone else is a participant.
    route: message.author?.id && message.author.id === own ? 'person' : 'external_participant',
    at: message.timestamp,
    edited: message.edited_timestamp,
    text: message.content ?? '',
  };
}

const newerFirst = (left: { id: string }, right: { id: string }): number =>
  BigInt(left.id) < BigInt(right.id) ? -1 : BigInt(left.id) > BigInt(right.id) ? 1 : 0;

export async function discordDriver(rawSdk: unknown, options: DriverOptions): Promise<Driver> {
  const sdk = rawSdk as Sdk;
  const manager = new sdk.DiscordCredentialManager(options.configDir);
  let active: SdkClient | null = null;
  let me: Author | null = null;
  let listed: Chat[] | null = null;
  let pending: Promise<void> | null = null;
  const guilds = new Map<string, string>();
  const direct = new Map<string, SdkDM>();

  const forget = (): void => {
    active = null;
    me = null;
    listed = null;
  };

  // signedIn opens the client only with the token the add-on's own data
  // directory holds. Calling the SDK's login without one would extract a token
  // from the desktop app or a browser, which this source never does.
  const signedIn = async (): Promise<SdkClient | null> => {
    if (active && me) return active;
    const token = await manager.getToken();
    if (!token) return null;
    const client = new sdk.DiscordClient();
    await client.login({ token });
    const profile = await client.testAuth();
    me = { id: profile.id, name: profile.global_name ?? profile.username };
    active = client;
    return client;
  };

  const require = async (): Promise<SdkClient> => {
    const client = await signedIn();
    if (!client) throw new Refusal('source_failing', 'needs_sign_in');
    return client;
  };

  const loadChats = async (client: SdkClient): Promise<Chat[]> => {
    const chats: Chat[] = [];
    guilds.clear();
    direct.clear();
    for (const guild of await client.listServers()) {
      for (const channel of await client.listChannels(guild.id)) {
        if (!READABLE.has(channel.type) || !SNOWFLAKE.test(channel.id)) continue;
        guilds.set(channel.id, guild.id);
        chats.push({ id: channel.id, name: `${guild.name} #${channel.name}`, kind: 'channel' });
      }
    }
    for (const dm of await client.listDMChannels()) {
      if (!SNOWFLAKE.test(dm.id)) continue;
      direct.set(dm.id, dm);
      const name = dm.name ?? dm.recipients.map(user => user.global_name ?? user.username).join(', ');
      chats.push({ id: dm.id, name: name || 'Direct message', kind: dm.type === 3 ? 'group' : 'direct' });
    }
    listed = chats.slice(0, LIMITS.chats);
    return listed;
  };

  const signInState = async (): Promise<SignInState> => {
    try {
      return (await signedIn()) ? 'signed_in' : 'needs_sign_in';
    } catch (error) {
      if (!isAuthError(error)) throw error;
      // A revoked session keeps its row; signing in again replaces the token.
      forget();
      return 'needs_sign_in';
    }
  };

  return {
    platform: 'discord',
    capabilities() {
      return cap({
        historyDepth: 'latest window',
        cursorKind: 'snowflake',
        edits: true,
        deletions: false,
        liveEvents: false,
        intervalSeconds: INTERVAL,
        readWindowSeconds: READ_WINDOW,
      });
    },
    signInState,
    async beginSignIn(): Promise<SignInStep> {
      try {
        if (await signedIn()) return signedInStep;
      } catch (error) {
        if (!isAuthError(error)) throw error;
        forget();
      }
      const shown = new Once();
      const flow: Promise<void> = sdk.loginWithRemoteAuth({
        onQrUrl: (url: string) => shown.set(url),
      }).then(async session => {
        // The QR session token goes to the add-on's own data directory and
        // nowhere else; Core keeps none of it.
        await manager.setToken(session.token);
        const client = new sdk.DiscordClient();
        await client.login({ token: session.token });
        const profile = await client.testAuth();
        me = { id: profile.id, name: profile.global_name ?? profile.username };
        active = client;
      });
      pending = flow.catch(() => {});
      const ended = flow.then(() => {
        throw new Refusal('source_failing', 'qr_missing');
      });
      ended.catch(() => {});
      const { promise: timeout, reject } = Promise.withResolvers<never>();
      setTimeout(() => reject(new Refusal('source_failing', 'qr_timeout')), QR_TIMEOUT_MS).unref?.();
      const url = await Promise.race([shown.wait(), ended, timeout]);
      return { state: 'needs_sign_in', show: { kind: 'url', value: url } };
    },
    async answer(): Promise<SignInStep> {
      if (pending) {
        await pending;
        pending = null;
      }
      const state = await signInState();
      return state === 'signed_in' ? signedInStep : { state };
    },
    async listChats() {
      const client = await require();
      return listed ?? (await loadChats(client));
    },
    async page(chat: string, cursor: string): Promise<Page> {
      if (!SNOWFLAKE.test(chat)) throw refused('chat');
      const client = await require();
      const own = me?.id ?? '';
      const after = cursor === '' ? '' : decodeCursor(cursor).after;
      if (cursor !== '' && (!after || !SNOWFLAKE.test(after))) throw refused('cursor');
      // The first read takes the newest window the SDK serves. A later read
      // takes the window around the newest message Core has: everything after
      // it, plus the messages just before it, so an edit to a recent message is
      // reported again as its new edited time. The store skips what is unchanged.
      const window = (await client.getMessages(chat, WINDOW, after ? { around: after } : undefined))
        .filter(entry => SNOWFLAKE.test(entry.id));
      window.sort(newerFirst);
      const newer = after ? window.filter(entry => newerFirst(entry, { id: after }) > 0) : window;
      const newest = window.at(-1);
      // A window of only messages past the cursor means more remain: walk
      // forward from the cursor until the window reaches back past it.
      if (cursor !== '' && newer.length >= NEWER_CAP) {
        return checkedPage({
          messages: newer.map(entry => wire(entry, own)),
          deletions: [],
          next: encodeCursor({ after: newer.at(-1)?.id ?? after }),
          hasMore: true,
          through: newer.at(-1)?.timestamp,
        });
      }
      const reached = newest && (!after || newerFirst(newest, { id: after }) > 0) ? newest.id : after;
      return checkedPage({
        messages: window.map(entry => wire(entry, own)),
        deletions: [],
        next: encodeCursor({ after: reached }),
        hasMore: false,
        through: newest?.timestamp,
      });
    },
    async members(chat: string): Promise<Author[]> {
      if (!SNOWFLAKE.test(chat)) throw refused('chat');
      const client = await require();
      if (!listed) await loadChats(client);
      const guild = guilds.get(chat);
      if (guild) {
        return (await client.listUsers(guild))
          .slice(0, LIMITS.members)
          .map(user => ({ id: user.id, name: user.global_name ?? user.username }));
      }
      return (direct.get(chat)?.recipients ?? []).map(user => ({ id: user.id, name: user.global_name ?? user.username }));
    },
    async whoami() {
      await require();
      return me ?? { id: '', name: '' };
    },
    live(): () => void {
      // The personal account reports no deletions or live messages through the
      // add-on's read operations; sync polls instead.
      return () => {};
    },
  };
}
