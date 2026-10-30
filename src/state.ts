import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";
import type { Entry } from "./plan";
import type { Rung } from "./upstream";

/** What the user last accepted: per route, the upstream chain and the routing written for it. */
export type State = {
  omoVersion: string;
  appliedAt: string;
  upstream: Record<string, Rung[]>;
  routing: Record<string, Entry[]>;
};

export function readState(path: string): State | undefined {
  return existsSync(path) ? (JSON.parse(readFileSync(path, "utf8")) as State) : undefined;
}

export function writeState(path: string, state: State): void {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `${JSON.stringify(state, null, 2)}\n`);
}
