import { accessSync, constants, existsSync, readFileSync, realpathSync } from "node:fs";
import { homedir } from "node:os";
import { delimiter, dirname, join } from "node:path";
import { OcxOmoError } from "./errors.js";

export type Rung = { model: string; variant?: string; providers: string[] };
export type Chains = {
  categories: Record<string, Rung[]>;
  agents: Record<string, Rung[]>;
};
export type Upstream = { path: string; version: string; chains: Chains };

const IDENT = "[A-Za-z_$][\\w$]*";
// omo <=5.1.66 wraps each category chain in a helper (`"visual-engineering":va([...])`); 5.1.69 writes the array directly.
const CATEGORY_ANCHOR = new RegExp(`=(\\{"visual-engineering":(?:${IDENT}\\()?\\[)`);
const AGENT_ANCHOR = /=(\{explore:\[\{providers:)/;

/** Return the balanced bracket expression starting at `start`, skipping string contents. */
function grabBalanced(source: string, start: number): string {
  let depth = 0;
  for (let index = start; index < source.length; index++) {
    const char = source[index];
    if (char === '"' || char === "'" || char === "`") {
      for (index++; index < source.length && source[index] !== char; index++) {
        if (source[index] === "\\") index++;
      }
    } else if (char === "{" || char === "[" || char === "(") depth++;
    else if (char === "}" || char === "]" || char === ")") {
      depth--;
      if (depth === 0) return source.slice(start, index + 1);
    }
  }
  throw new OcxOmoError("unbalanced object literal in omo-task.js");
}

const withoutStrings = (text: string) => text.replace(/"(?:[^"\\]|\\.)*"/g, '""');

/** `text` as a RegExp that matches it literally: every metacharacter, the backslash included, is escaped. */
export const escapeRegExp = (text: string) => text.replace(/[\\^$.*+?()[\]{}|\/-]/g, "\\$&");

/** Find the literal assigned to `name` that defines every property the chains read from it. */
function resolveConstant(source: string, name: string, props: Set<string>): unknown {
  const assignment = new RegExp(`(?:^|[,;{(\\s])${escapeRegExp(name)}=\\{`, "g");
  for (const match of source.matchAll(assignment)) {
    const start = match.index + match[0].length - 1;
    try {
      const value: unknown = new Function(`return (${grabBalanced(source, start)})`)();
      const record = value as Record<string, unknown>;
      if ([...props].every((prop) => typeof record[prop] === "string")) return value;
    } catch {
      // Same minified name in another scope with a non-literal body: try the next assignment.
    }
  }
  throw new OcxOmoError(`cannot resolve constant ${name} (${[...props].join(", ")}) in omo-task.js`);
}

function evaluateChains(source: string, literal: string, label: string): Record<string, Rung[]> {
  const bare = withoutStrings(literal);
  const wrappers = new Set([...bare.matchAll(new RegExp(`(${IDENT})\\(\\[`, "g"))].map((m) => m[1] as string));
  const constants = new Map<string, Set<string>>();
  for (const match of bare.matchAll(new RegExp(`(?<![\\w$.])(${IDENT})\\.(${IDENT})`, "g"))) {
    const [, name, prop] = match as unknown as [string, string, string];
    constants.set(name, (constants.get(name) ?? new Set()).add(prop));
  }
  const names = [...wrappers, ...constants.keys()];
  const values = [
    ...[...wrappers].map(() => (rungs: Rung[]) => rungs),
    ...[...constants].map(([name, props]) => resolveConstant(source, name, props)),
  ];
  let result: unknown;
  try {
    result = new Function(...names, `return (${literal})`)(...values);
  } catch (error) {
    throw new OcxOmoError(`cannot evaluate ${label} chains in omo-task.js: ${String(error)}`);
  }
  return validateChains(result, label);
}

function validateChains(value: unknown, label: string): Record<string, Rung[]> {
  if (typeof value !== "object" || value === null)
    throw new OcxOmoError(`${label} chains are not an object`);
  for (const [name, chain] of Object.entries(value)) {
    const valid =
      Array.isArray(chain) &&
      chain.length > 0 &&
      chain.every(
        (rung: Partial<Rung>) => typeof rung.model === "string" && Array.isArray(rung.providers),
      );
    if (!valid) throw new OcxOmoError(`${label} chain "${name}" has an unknown shape`);
  }
  return value as Record<string, Rung[]>;
}

export function extractChains(source: string): Chains {
  const extract = (anchor: RegExp, label: string) => {
    const match = anchor.exec(source);
    if (!match) throw new OcxOmoError(`${label} model chains not found in omo-task.js; its format changed`);
    const literal = grabBalanced(source, match.index + 1);
    return evaluateChains(source, literal, label);
  };
  return { categories: extract(CATEGORY_ANCHOR, "category"), agents: extract(AGENT_ANCHOR, "agent") };
}

/** The first executable `name` on PATH, like `which`. */
function onPath(name: string): string | undefined {
  for (const directory of (process.env["PATH"] ?? "").split(delimiter)) {
    if (!directory) continue;
    const candidate = join(directory, name);
    try {
      accessSync(candidate, constants.X_OK);
      return candidate;
    } catch {
      // not here: try the next PATH entry
    }
  }
  return undefined;
}

function candidates(): string[] {
  const found: string[] = [];
  const fromBinary = (binary: string) => {
    let real = binary;
    try {
      real = realpathSync(binary);
    } catch {
      return;
    }
    const directory = dirname(real);
    found.push(join(directory, "plugin/extensions/omo-task.js"));
    found.push(join(directory, "../plugin/extensions/omo-task.js"));
  };
  if (process.env["OMO_BIN"]) fromBinary(process.env["OMO_BIN"]);
  const omo = onPath("omo");
  if (omo) fromBinary(omo);
  found.push(join(homedir(), ".bun/install/global/node_modules/omo-ai/plugin/extensions/omo-task.js"));
  found.push("/Applications/OmO.app/Contents/Resources/omo-runtime/darwin-arm64/plugin/extensions/omo-task.js");
  return found;
}

export function locateOmoTask(explicit?: string): string {
  const paths = explicit ? [explicit] : candidates();
  const path = paths.find((candidate) => existsSync(candidate));
  if (!path) throw new OcxOmoError(`omo-task.js not found; pass --omo-task. Tried: ${paths.join(", ")}`);
  return path;
}

function readVersion(taskPath: string): string {
  const manifest = join(dirname(taskPath), "..", "package.json");
  if (!existsSync(manifest)) return "unknown";
  const parsed = JSON.parse(readFileSync(manifest, "utf8")) as { version?: string };
  return parsed.version ?? "unknown";
}

export function loadUpstream(explicit?: string): Upstream {
  const path = locateOmoTask(explicit);
  return { path, version: readVersion(path), chains: extractChains(readFileSync(path, "utf8")) };
}
