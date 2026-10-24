// SPDX-License-Identifier: Apache-2.0

// The add-on's wire format: one JSON object per line, UTF-8, at most 8 MiB.
// Core sends requests and reads responses; the only unsolicited frames are live
// events, and only while a live subscription is active. A subscription that
// ends on its own sends one last `{"event":"ended","error":<code>}` frame.

export const PROTOCOL = 1;
export const MAX_LINE = 8 * 1024 * 1024;
export const MAX_PAGE = 500;
export const MAX_CHATS = 10_000;
export const MAX_MEMBERS = 5_000;

export type Route = 'person' | 'automation' | 'system' | 'external_participant';

export type Author = { id: string; name: string };

export type Attachment = { id: string; name: string; mimeType: string; size: number };

export type Chat = { id: string; name: string; kind: string; firstMessageAt?: string; lastMessageId?: string };

export type Message = {
  id: string;
  author: Author;
  route: Route;
  at: string;
  edited?: string;
  text: string;
  // decryptionFailed marks a message the platform served but this account
  // could not decrypt. It keeps the record with no text; it is never an empty
  // message that reads as something the person wrote.
  decryptionFailed?: boolean;
  replyTo?: string;
  thread?: string;
  attachments?: Attachment[];
};

export type Deletion = { message: string; at: string };

// Coverage states what a completed read covers when the platform serves no
// position that outlives the process or a bounded range. Core stores it with the
// chat's coverage; an absent one keeps the adapter's declared default.
export type Coverage = { historyDepth: string; cursorSurvivesRestart: boolean };

export type Page = {
  messages: Message[];
  deletions: Deletion[];
  next: string;
  hasMore: boolean;
  through?: string;
  coverage?: Coverage;
};

export type Capabilities = {
  historyDepth: string;
  cursorKind: string;
  edits: boolean;
  deletions: boolean;
  liveEvents: boolean;
  intervalSeconds: number;
  readWindowSeconds: number;
};

export type SignInState = 'signed_in' | 'needs_sign_in' | 'failing';

export type Prompt = { key: string; kind: 'text' | 'secret'; text: string };

// A sign-in step either asks for typed input or shows something the person uses
// on the platform (a QR payload, a passcode, a pairing code). The add-on relays
// the value and keeps none of it.
export type SignInStep = { state: SignInState; prompt?: Prompt; show?: { kind: 'url' | 'code'; value: string } };

// A live event names the chat it belongs to; a push carries no page position,
// so Core admits it without moving that chat's cursor.
export type LiveEvent =
  | { event: 'message'; chat: string; message: Message }
  | { event: 'deletion'; chat: string; deletion: Deletion };

export type Driver = {
  readonly platform: string;
  capabilities(): Capabilities;
  signInState(): Promise<SignInState>;
  beginSignIn(): Promise<SignInStep>;
  answer(key: string, value: string): Promise<SignInStep>;
  listChats(): Promise<Chat[]>;
  page(chat: string, cursor: string): Promise<Page>;
  members(chat: string): Promise<Author[]>;
  whoami(): Promise<Author>;
  // live calls ended once, with the cause, when the subscription stops without
  // being stopped: a lost connection, a revoked session or a closed stream.
  live(deliver: (event: LiveEvent) => void, ended: (error: unknown) => void): () => void;
};

// OPS is the whole dispatch table. Every entry is a read, a sign-in step or a
// control request; there is no operation that names an SDK method.
export const OPS = Object.freeze([
  'hello', 'signIn', 'beginSignIn', 'answer', 'listChats', 'page', 'members', 'whoami', 'live', 'stop', 'quit',
] as const);

export type Op = (typeof OPS)[number];

const FIELDS: Record<Op, readonly string[]> = {
  hello: ['id', 'op'],
  signIn: ['id', 'op'],
  beginSignIn: ['id', 'op'],
  answer: ['id', 'op', 'prompt', 'value'],
  listChats: ['id', 'op'],
  page: ['id', 'op', 'chat', 'cursor'],
  members: ['id', 'op', 'chat'],
  whoami: ['id', 'op'],
  live: ['id', 'op'],
  stop: ['id', 'op'],
  quit: ['id', 'op'],
};

// Request is the validated shape. parseRequest is the only producer, so a
// field an operation does not carry is absent here too.
export type Request =
  | { id: string; op: 'answer'; prompt: string; value: string }
  | { id: string; op: 'page'; chat: string; cursor: string }
  | { id: string; op: 'members'; chat: string }
  | { id: string; op: Exclude<Op, 'answer' | 'page' | 'members'> };

const identifier = (value: unknown): value is string =>
  typeof value === 'string' && /^[A-Za-z0-9_.:-]{1,128}$/.test(value);

// parseRequest refuses anything outside the fixed table: an unknown operation,
// an unknown field, a missing identifier or a value of the wrong type.
export function parseRequest(text: string): Request {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new Refusal('invalid_request', 'not JSON');
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Refusal('invalid_request', 'not an object');
  const record = value as Record<string, unknown>;
  if (!identifier(record.id)) throw new Refusal('invalid_request', 'id');
  if (typeof record.op !== 'string' || !(OPS as readonly string[]).includes(record.op)) {
    throw new Refusal('unknown_operation', 'operation not offered');
  }
  const op = record.op as Op;
  for (const key of Object.keys(record)) {
    if (!FIELDS[op].includes(key)) throw new Refusal('invalid_request', `unexpected field ${key}`);
  }
  if ((op === 'page' || op === 'members') && !identifier(record.chat)) throw new Refusal('invalid_request', 'chat');
  if (op === 'page' && typeof record.cursor !== 'string') throw new Refusal('invalid_request', 'cursor');
  if (op === 'page' && record.cursor.length > 4096) throw new Refusal('invalid_request', 'cursor');
  if (op === 'answer') {
    if (!identifier(record.prompt) || typeof record.value !== 'string' || record.value.length > 4096) {
      throw new Refusal('invalid_request', 'answer');
    }
  }
  return record as unknown as Request;
}

export class Refusal extends Error {
  code: string;
  constructor(code: string, message: string) {
    super(message);
    this.code = code;
  }
}

// Frames are serialized once so a concurrent live event and a response cannot
// interleave inside one line.
export function frame(value: unknown): string {
  return JSON.stringify(value) + '\n';
}
