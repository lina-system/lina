// SPDX-License-Identifier: Apache-2.0

// The WhatsApp driver. WhatsApp reaches LINA through a linked device: the phone
// pushes its history once at link time and then live messages. Upstream keeps at
// most 500 messages per chat in memory, so a read serves what the phone pushed
// and a live subscription polls that buffer before it rolls over. Nothing here
// sends, edits, reacts or marks anything read.
//
// Pairing code sign-in is offered here (WhatsApp > Linked Devices > Link with
// phone number). A QR payload would need a new display kind in the add-on's
// protocol, so it stays with upstream's own command for now.
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { at, cap, decodeCursor, encodeCursor, needsSignIn, signedIn, type DriverOptions } from './driver.ts';
import { Refusal, type Author, type Chat, type Driver, type LiveEvent, type Message, type Page, type SignInStep, type SignInState } from './protocol.ts';

// Page size the Core is served at. The SDK holds at most 500 messages per chat,
// so one chat needs at most three pages.
const PAGE = 200;
const BUFFER = 500;
const POLL_MS = 1_000;

type Summary = {
  id: string;
  chat_id: string;
  from: string;
  from_name?: string;
  timestamp: string;
  is_outgoing: boolean;
  type: string;
  text?: string;
};

type ChatSummary = { id: string; name: string; type: string };

type Profile = { id: string; name: string | null; phone_number: string | null };

// Client is the slice of the pinned SDK this driver uses. Every method is a read
// or the account's own sign-in; the contract test proves the list.
type Client = {
  login(credentials: { authDir: string }): Promise<Client>;
  connect(): Promise<void>;
  connectForPairing(phone: string): Promise<{ code: string; waitForAuth: () => Promise<void> }>;
  listChats(limit?: number): Promise<ChatSummary[]>;
  getMessages(chat: string, limit?: number): Promise<Summary[]>;
  getProfile(): Promise<Profile>;
  close(): Promise<void>;
};

type Sdk = { WhatsAppClient: new () => Client };

const typeKind = (type: string): string | null => {
  switch (type) {
    case 'individual':
    case 'direct':
      return 'direct';
    case 'group':
      return 'group';
    case 'broadcast':
    case 'channel':
      return 'channel';
    case 'status':
      return null;
    default:
      return null;
  }
};

const stamp = (value: string): number => {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : 0;
};

// Position names the last message a caller has seen. Messages that share its
// timestamp stay distinguishable by id, so a page never skips one of them.
type Position = { at: string; ids: string[] };

function decodePosition(cursor: string): Position | null {
  const value = decodeCursor(cursor);
  if (!value.at || !Number.isFinite(Date.parse(value.at))) return null;
  return { at: value.at, ids: value.ids ? value.ids.split(',') : [] };
}

function after(entry: Summary, position: Position): boolean {
  const left = stamp(entry.timestamp);
  const right = stamp(position.at);
  return left > right || (left === right && !position.ids.includes(entry.id));
}

function positionOf(entries: Summary[], index: number): Position {
  const value = entries[index];
  return { at: value.timestamp, ids: entries.filter(entry => entry.timestamp === value.timestamp).map(entry => entry.id) };
}

const encodePosition = (position: Position): string => encodeCursor({ at: position.at, ids: position.ids.join(',') });

// refusal keeps an SDK failure to its platform code: a message can carry host
// details, and only a forbidden account changes what LINA tells the person.
function refusal(error: unknown): Error {
  const code = error instanceof Error && 'code' in error && typeof error.code === 'string' ? error.code : '';
  if (code === 'forbidden') return new Refusal('source_forbidden', 'WhatsApp restricted this account');
  if (code === 'logged_out') return new Refusal('source_logged_out', 'WhatsApp signed this linked device out');
  return error instanceof Error ? error : new Refusal('source_failing', 'WhatsApp read failed');
}

export function whatsappDriver(sdk: unknown, options: DriverOptions): Driver {
  const { WhatsAppClient } = sdk as Sdk;
  const authDir = join(options.configDir, 'whatsapp');
  const pollMs = options.liveMs ?? POLL_MS;
  const now = options.now ?? (() => new Date());

  let client: Client | null = null;
  let profile: Profile | null = null;
  let connected = false;
  // connecting is the connect in flight, so two reads arriving together open
  // one socket rather than two.
  let connecting: Promise<Client> | null = null;
  let pairing: Promise<void> | null = null;
  let pairingState: SignInState | null = null;

  // reset forgets a socket that failed, so the next read logs in again instead
  // of reusing it.
  const reset = (): void => {
    client, connected = null, false;
  };

  const connectedClient = async (): Promise<Client> => {
    if (client && connected) return client;
    if (connecting) return connecting;
    connecting = (async () => {
      const value = await new WhatsAppClient().login({ authDir });
      try {
        await value.connect();
      } catch (error) {
        await value.close().catch(() => {});
        throw refusal(error);
      }
      client, connected = value, true;
      return value;
    })();
    try {
      return await connecting;
    } finally {
      connecting = null;
    }
  };

  // attempt keeps an SDK failure to its platform code, so a revoked link and a
  // refusal are told apart from a dropped socket.
  const attempt = async <T>(work: () => Promise<T>): Promise<T> => {
    try {
      return await work();
    } catch (error) {
      throw refusal(error);
    }
  };

  const me = async (): Promise<Profile> => {
    if (profile) return profile;
    const value = await connectedClient();
    profile = await attempt(() => value.getProfile());
    return profile;
  };

  const author = (entry: Summary, self: Profile): Author => {
    const id = entry.is_outgoing ? self.id : entry.from || entry.chat_id;
    return { id, name: entry.from_name ?? (entry.is_outgoing ? (self.name ?? '') : '') };
  };

  const convert = (entry: Summary, self: Profile): Message => {
    const message: Message = {
      id: entry.id,
      author: author(entry, self),
      route: entry.is_outgoing ? 'person' : 'external_participant',
      at: at(entry.timestamp, now()),
      text: entry.text ?? '',
    };
    return message;
  };

  const readBuffer = async (chat: string): Promise<Summary[]> => {
    const value = await connectedClient();
    const entries = await attempt(() => value.getMessages(chat, BUFFER));
    return [...entries].sort((left, right) => stamp(left.timestamp) - stamp(right.timestamp) || (left.id < right.id ? -1 : left.id > right.id ? 1 : 0));
  };

  const driver: Driver = {
    platform: 'whatsapp',

    capabilities() {
      return cap({
        // The phone pushes its history once at link time; nothing older is
        // reachable later. The SDK holds only what arrived while this process
        // runs, so a read gap means missed messages: the window is two
        // intervals, not a day.
        historyDepth: 'link-time',
        cursorKind: 'position',
        edits: false,
        deletions: false,
        liveEvents: true,
        intervalSeconds: 900,
        readWindowSeconds: 1_800,
      });
    },

    async signInState(): Promise<SignInState> {
      if (pairingState) return pairingState;
      if (!existsSync(join(authDir, 'creds.json'))) return 'needs_sign_in';
      try {
        await me();
      } catch (error) {
        // A revoked link is the one cause that shows the sign-in screen; a
        // refusal or a failed socket keeps its own cause.
        if (error instanceof Refusal && error.code === 'source_logged_out') {
          reset();
          return 'needs_sign_in';
        }
        throw error;
      }
      return 'signed_in';
    },

    async beginSignIn(): Promise<SignInStep> {
      const state = await driver.signInState();
      if (state === 'signed_in') return signedIn();
      return needsSignIn({ key: 'phone', kind: 'text', text: 'Phone number of the WhatsApp account' });
    },

    async answer(key: string, value: string): Promise<SignInStep> {
      if (key !== 'phone') throw new Refusal('invalid_request', 'unknown sign-in prompt');
      const phone = value.trim();
      if (!/^\+?[0-9][0-9 ()-]{4,}$/.test(phone)) throw new Refusal('invalid_request', 'phone number');
      const value0 = await new WhatsAppClient().login({ authDir });
      let code: string;
      let waitForAuth: () => Promise<void>;
      try {
        ({ code, waitForAuth } = await value0.connectForPairing(phone));
      } catch (error) {
        await value0.close().catch(() => {});
        throw refusal(error);
      }
      client, connected = value0, false;
      pairingState = 'needs_sign_in';
      pairing = waitForAuth().then(
        () => {
          connected = true;
          pairingState = 'signed_in';
        },
        error => {
          pairingState = 'failing';
          throw refusal(error);
        },
      );
      // A pairing that fails later must not become an unhandled rejection.
      void pairing.catch(() => {});
      const formatted = code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
      return { state: 'needs_sign_in', show: { kind: 'code', value: formatted } };
    },

    async listChats(): Promise<Chat[]> {
      const value = await connectedClient();
      const chats = await attempt(() => value.listChats());
      const out: Chat[] = [];
      for (const chat of chats) {
        const kind = typeKind(chat.type);
        if (!kind || !chat.id) continue;
        out.push({ id: chat.id, name: chat.name || chat.id, kind });
      }
      return out;
    },

    async page(chat: string, cursor: string): Promise<Page> {
      const self = await me();
      const entries = await readBuffer(chat);
      const position = decodePosition(cursor);
      const start = position ? entries.findIndex(entry => after(entry, position)) : 0;
      const remaining = start < 0 ? [] : entries.slice(start);
      const slice = remaining.slice(0, PAGE);
      const hasMore = remaining.length > slice.length;
      const next = slice.length > 0 ? positionOf(remaining, slice.length - 1) : position;
      return {
        messages: slice.map(entry => convert(entry, self)),
        deletions: [],
        next: next ? encodePosition(next) : '',
        hasMore,
        through: hasMore ? undefined : entries.length > 0 ? entries[entries.length - 1].timestamp : now().toISOString(),
      };
    },

    async members(chat: string): Promise<Author[]> {
      const self = await me();
      const entries = await readBuffer(chat);
      const seen = new Map<string, Author>();
      seen.set(self.id, { id: self.id, name: self.name ?? '' });
      for (const entry of entries) {
        const value = author(entry, self);
        if (value.id) seen.set(value.id, value);
      }
      return [...seen.values()];
    },

    async whoami(): Promise<Author> {
      const self = await me();
      return { id: self.id, name: self.name ?? '' };
    },

    live(deliver: (event: LiveEvent) => void): () => void {
      let stopped = false;
      const known = new Map<string, Set<string>>();
      let timer: NodeJS.Timeout | null = null;

      // The subscription starts from what is already buffered: those messages
      // reach Core through the scheduled page read, not as live events. A
      // message that arrives between a committed page and this seed is still in
      // the buffer, and the next page read carries it.
      const seed = async (): Promise<void> => {
        const value = await connectedClient();
        const chats = await value.listChats();
        for (const chat of chats) {
          if (stopped) return;
          const entries = await readBuffer(chat.id);
          known.set(chat.id, new Set(entries.map(entry => entry.id)));
        }
      };

      const poll = async (): Promise<void> => {
        if (stopped) return;
        try {
          const self = await me();
          for (const [chat, seen] of known) {
            const entries = await readBuffer(chat);
            for (const entry of entries) {
              if (seen.has(entry.id)) continue;
              seen.add(entry.id);
              deliver({ event: 'message', chat, message: convert(entry, self) });
            }
          }
        } catch {
          // A dropped socket is forgotten rather than reused; the next poll logs
          // in again, and the scheduled read fills anything missed meanwhile.
          reset();
        }
        if (stopped) return;
        timer = setTimeout(() => void poll(), pollMs);
      };

      void seed().then(
        () => {
          if (!stopped) timer = setTimeout(() => void poll(), pollMs);
        },
        () => {
          // Seeding failed; the scheduled read still covers the chat.
        },
      );
      return () => {
        stopped = true;
        clearTimeout(timer);
        timer = null;
      };
    },

    async close(): Promise<void> {
      if (client) await client.close().catch(() => {});
      reset();
    },
  };

  return driver;
}
