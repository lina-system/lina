// SPDX-License-Identifier: Apache-2.0

// Shape checks for what the SDK returns. A platform response the add-on does
// not recognise is refused as `upstream_changed` instead of being passed on as
// a record: Core then fails that source's batch and keeps its cursor.
import { Refusal, type Author, type Chat, type Message, type Page, type Route } from './protocol.ts';


const ROUTES: Record<Route, true> = { person: true, automation: true, system: true, external_participant: true };

const text = (value: unknown): value is string => typeof value === 'string';
const id = (value: unknown): value is string => typeof value === 'string' && value.length > 0 && value.length <= 512;
const changed = (what: string) => new Refusal('upstream_changed', what);

export function author(value: unknown): Author {
  if (!value || typeof value !== 'object') throw changed('author');
  const source = value as Record<string, unknown>;
  if (!id(source.id) || typeof source.name !== 'string') throw changed('author');
  return { id: source.id as string, name: source.name };
}

export function time(value: unknown): string {
  if (!text(value) || !Number.isFinite(Date.parse(value))) throw changed('time');
  return new Date(value).toISOString();
}

export function chat(value: unknown): Chat {
  if (!value || typeof value !== 'object') throw changed('chat');
  const source = value as Record<string, unknown>;
  if (!id(source.id) || typeof source.name !== 'string' || typeof source.kind !== 'string') throw changed('chat');
  return {
    id: source.id as string,
    name: source.name,
    kind: source.kind,
    firstMessageAt: source.firstMessageAt === undefined ? undefined : time(source.firstMessageAt),
  };
}

export function message(value: unknown): Message {
  if (!value || typeof value !== 'object') throw changed('message');
  const source = value as Record<string, unknown>;
  if (!id(source.id) || typeof source.route !== 'string' || !Object.hasOwn(ROUTES, source.route)) throw changed('message');
  if (typeof source.text !== 'string') throw changed('message');
  const result: Message = {
    id: source.id as string,
    author: author(source.author),
    route: source.route as Route,
    at: time(source.at),
    text: source.text,
  };
  if (source.edited !== undefined && source.edited !== '') result.edited = time(source.edited);
  if (source.decryptionFailed !== undefined) {
    if (typeof source.decryptionFailed !== 'boolean') throw changed('message');
    if (source.decryptionFailed) {
      // A failed decryption has no text; it never becomes an empty message.
      if (result.text !== '') throw changed('message');
      result.decryptionFailed = true;
    }
  }
  if (source.replyTo !== undefined) {
    if (!text(source.replyTo)) throw changed('message');
    result.replyTo = source.replyTo;
  }
  if (source.thread !== undefined) {
    if (!text(source.thread)) throw changed('message');
    result.thread = source.thread;
  }
  if (source.attachments !== undefined) {
    if (!Array.isArray(source.attachments)) throw changed('attachment');
    result.attachments = source.attachments.map(entry => {
      if (!entry || typeof entry !== 'object') throw changed('attachment');
      const file = entry as Record<string, unknown>;
      if (!id(file.id) || typeof file.name !== 'string' || typeof file.mimeType !== 'string' || typeof file.size !== 'number') {
        throw changed('attachment');
      }
      return { id: file.id as string, name: file.name, mimeType: file.mimeType, size: file.size };
    });
  }
  return result;
}

export function page(value: unknown): Page {
  if (!value || typeof value !== 'object') throw changed('page');
  const source = value as Record<string, unknown>;
  if (!Array.isArray(source.messages) || !Array.isArray(source.deletions)) throw changed('page');
  if (typeof source.next !== 'string' || typeof source.hasMore !== 'boolean') throw changed('page');
  const deletions = source.deletions.map(entry => {
    if (!entry || typeof entry !== 'object') throw changed('page');
    const deletion = entry as Record<string, unknown>;
    if (!id(deletion.message)) throw changed('page');
    return { message: deletion.message as string, at: time(deletion.at) };
  });
  const page: Page = {
    messages: source.messages.map(message),
    deletions,
    next: source.next,
    hasMore: source.hasMore,
    through: source.through === undefined ? undefined : time(source.through),
  };
  if (source.coverage !== undefined) {
    const coverage = source.coverage;
    if (!coverage || typeof coverage !== 'object' || Array.isArray(coverage)) throw changed('coverage');
    const entry = coverage as Record<string, unknown>;
    if (typeof entry.historyDepth !== 'string' || typeof entry.cursorSurvivesRestart !== 'boolean') {
      throw changed('coverage');
    }
    page.coverage = { historyDepth: entry.historyDepth, cursorSurvivesRestart: entry.cursorSurvivesRestart };
  }
  return page;
}
