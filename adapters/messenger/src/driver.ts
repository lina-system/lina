// SPDX-License-Identifier: Apache-2.0

// Shared pieces for every platform driver: the opaque cursor, the size limits,
// and the capability defaults. A driver names only the reads it performs; the
// contract test records every SDK method call and proves the list.
import type { Author, Capabilities, Chat, Driver, LiveEvent, Page, SignInStep } from './protocol.ts';

export type DriverOptions = {
  // configDir is the add-on's data directory for this account (0700).
  configDir: string;
  now?: () => Date;
};

export type DriverFactory = (sdk: unknown, options: DriverOptions) => Driver;

const ISO = (value: unknown): string | null => {
  const date = typeof value === 'number' ? new Date(value) : typeof value === 'string' ? new Date(value) : null;
  return date && Number.isFinite(date.getTime()) ? date.toISOString() : null;
};

export const at = (value: unknown, fallback: Date): string => ISO(value) ?? fallback.toISOString();

export const LIMITS = { chats: 10_000, page: 500, members: 5_000 } as const;

// Pages read oldest first from an opaque cursor. The empty cursor starts at the
// oldest end the platform serves.
export function decodeCursor(cursor: string): Record<string, string> {
  if (cursor === '' || cursor.length > 4096) return {};
  try {
    const value: unknown = JSON.parse(cursor);
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
    const result: Record<string, string> = {};
    for (const [key, entry] of Object.entries(value)) {
      if (typeof entry !== 'string') return {};
      result[key] = entry;
    }
    return result;
  } catch {
    return {};
  }
}

export const encodeCursor = (value: Record<string, string>): string => JSON.stringify(value);

export function cap(value: Partial<Capabilities>): Capabilities {
  return {
    historyDepth: 'platform-window',
    cursorKind: 'opaque',
    edits: true,
    deletions: false,
    liveEvents: false,
    intervalSeconds: 900,
    readWindowSeconds: 172_800,
    ...value,
  };
}

export function clamp<T>(values: readonly T[], limit: number): T[] {
  return values.slice(0, limit);
}

export const signedIn = (): SignInStep => ({ state: 'signed_in' });

export const needsSignIn = (prompt: SignInStep['prompt']): SignInStep => ({ state: 'needs_sign_in', prompt });

// Once answers a promise when the platform reports the value, so a driver can
// show a passcode or QR the person uses on another device.
export class Once {
  private readonly promise: Promise<string>;
  private resolve!: (value: string) => void;
  private settled = false;
  constructor() {
    this.promise = new Promise(resolve => {
      this.resolve = resolve;
    });
  }
  set(value: string): void {
    if (this.settled) return;
    this.settled = true;
    this.resolve(value);
  }
  wait(): Promise<string> {
    return this.promise;
  }
}

export type { Author, Chat, Driver, Page, LiveEvent };
