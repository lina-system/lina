// SPDX-License-Identifier: Apache-2.0

// A recorded synthetic platform for the add-on's tests and its end-to-end run.
// It answers from a recorded script, so nothing here contacts a platform, reads
// a session or installs an SDK.
//
// The script is JSON in the add-on's data directory (`script.json`):
//
//	{ "chats": [...], "pages": { "<cursor>": <page> },
//	  "blockCursor": "<cursor>", "onPage": ... }
//
// blockCursor makes the first page read of that cursor hang forever and signal
// readiness, then self-terminate when the test writes a kill marker.
import { readFileSync, writeFileSync, existsSync, watch } from 'node:fs';
import { join } from 'node:path';
import type { Author, Capabilities, Chat, Driver, LiveEvent, Page, SignInStep, SignInState } from './src/protocol.ts';
import { cap } from './src/driver.ts';

export type Recorded = {
  signIn?: SignInState;
  chats?: Chat[];
  page?: unknown;
  pages?: Record<string, unknown>;
  members?: Author[];
  whoami?: Author;
  blockCursor?: string;
  configDir?: string;
};

export type FixtureDriver = Driver & {
  calls: string[];
  delivered: LiveEvent[];
  emit(event: LiveEvent): void;
};

export function fixtureDriver(recorded: Recorded = {}): FixtureDriver {
  const calls: string[] = [];
  const delivered: LiveEvent[] = [];
  let sink: ((event: LiveEvent) => void) | null = null;
  const note = <T>(name: string, value: T): T => {
    calls.push(name);
    return value;
  };
  const blocked = new Set<string>();
  return {
    platform: 'fixture',
    calls,
    delivered,
    emit(event) {
      delivered.push(event);
      if (sink) sink(event);
    },
    capabilities(): Capabilities {
      return cap({ historyDepth: 'all' });
    },
    async signInState(): Promise<SignInState> {
      return note('signIn', recorded.signIn ?? 'signed_in');
    },
    async beginSignIn(): Promise<SignInStep> {
      return note('beginSignIn', { state: 'signed_in' } as SignInStep);
    },
    async answer(): Promise<SignInStep> {
      return note('answer', { state: 'signed_in' } as SignInStep);
    },
    async listChats(): Promise<Chat[]> {
      return note('listChats', recorded.chats ?? [{ id: 'dm-1', name: 'Direct', kind: 'direct' }]);
    },
    async page(_chat: string, cursor: string): Promise<Page> {
      calls.push('page');
      if (recorded.blockCursor === cursor && recorded.configDir && !blocked.has(cursor) && !existsSync(join(recorded.configDir, "blocked"))) {
        blocked.add(cursor);
        // Signal only this fixture process inside its PID namespace. Never
        // export a namespace PID for the host to kill.
        const dir = recorded.configDir;
        await new Promise(() => {
          const die = () => { if (existsSync(join(dir, 'kill'))) process.kill(process.pid, 'SIGKILL'); };
          watch(dir, die);
          writeFileSync(join(dir, 'blocked'), 'ready');
          die();
        });
      }
      const pages = recorded.pages ?? {};
      if (Object.hasOwn(pages, cursor)) return pages[cursor] as Page;
      return (recorded.page ?? {
        messages: [{ id: 'm-1', author: { id: 'u1', name: 'Someone' }, route: 'external_participant', at: '2026-10-24T09:00:00.000Z', text: 'hello' }],
        deletions: [],
        next: 'c1',
        hasMore: true,
      }) as Page;
    },
    async members(): Promise<Author[]> {
      return note('members', recorded.members ?? [{ id: 'u1', name: 'Someone' }]);
    },
    async whoami(): Promise<Author> {
      return note('whoami', recorded.whoami ?? { id: 'u0', name: 'Me' });
    },
    live(deliver: (event: LiveEvent) => void): () => void {
      note('live', true);
      sink = deliver;
      return () => {
        note('stop', true);
        sink = null;
      };
    },
  };
}

// recorded reads the script from the add-on's data directory when Core points
// the process at one.
export function recordedFrom(configDir: string): Recorded {
  try {
    return JSON.parse(readFileSync(join(configDir, 'script.json'), 'utf8')) as Recorded;
  } catch {
    return {};
  }
}
