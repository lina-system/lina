import { copyFileSync, existsSync, readFileSync, renameSync, statSync, writeFileSync } from "node:fs";
import { applyEdits, modify, type JSONPath } from "jsonc-parser";
import { OcxOmoError } from "./errors";
import type { Entry, Plan } from "./plan";

/** The only keys ocx-omo owns on a route; prompts, tools and other tuning stay untouched. */
const ROUTING_KEYS = ["model", "models", "reasoning", "variant", "reasoningEffort", "fallback_models"];
const FORMAT = { formattingOptions: { insertSpaces: true, tabSize: 2 } };

type Route = Record<string, unknown>;
export type ConfigFile = { path: string; text: string; parsed: Record<string, unknown> };

export function readConfig(path: string): ConfigFile {
  const text = existsSync(path) ? readFileSync(path, "utf8") : "{}\n";
  const parsed = Bun.JSONC.parse(text);
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed))
    throw new OcxOmoError(`${path} must be a JSONC object`);
  return { path, text, parsed: parsed as Record<string, unknown> };
}

const splitPath = (path: string): [string, string] => {
  const dot = path.indexOf(".");
  return [path.slice(0, dot), path.slice(dot + 1)];
};

function routeOf(config: Record<string, unknown>, path: string): Route | undefined {
  const [scope, name] = splitPath(path);
  return (config[scope] as Record<string, Route> | undefined)?.[name];
}

/** The route's model list as configured; a lone `model` reads as a one-entry list. */
export function currentEntries(config: Record<string, unknown>, path: string): Entry[] {
  const route = routeOf(config, path);
  if (Array.isArray(route?.["models"])) {
    return (route["models"] as (string | Entry)[]).map((entry) =>
      typeof entry === "string" ? { model: entry } : entry,
    );
  }
  if (typeof route?.["model"] !== "string") return [];
  const reasoning = route["reasoning"];
  return [{ model: route["model"], ...(typeof reasoning === "string" ? { reasoning } : {}) }];
}

export function nativeOverrideWarnings(config: Record<string, unknown>): string[] {
  const native = config["[native]"] as Route | undefined;
  return ["categories", "agents"]
    .filter((key) => native?.[key] !== undefined)
    .map((key) => `[native].${key} exists in omo.jsonc and may take precedence over top-level ${key}`);
}

export function editRouting(text: string, plan: Plan, paths: string[]): string {
  let result = text;
  const edit = (path: JSONPath, value: unknown) => {
    result = applyEdits(result, modify(result, path, value, FORMAT));
  };
  for (const path of paths) {
    const planned = plan.routes[path];
    if (!planned?.models.length) continue;
    const [scope, name] = splitPath(path);
    const route = routeOf(Bun.JSONC.parse(result) as Record<string, unknown>, path) ?? {};
    for (const key of ROUTING_KEYS) {
      if (key !== "models" && key in route) edit([scope, name, key], undefined);
    }
    edit([scope, name, "models"], planned.models);
  }
  return result;
}

/** Keep the previous file beside it, then replace it atomically. Returns the backup path. */
export function writeConfig(path: string, text: string): string | undefined {
  const mode = existsSync(path) ? statSync(path).mode & 0o777 : 0o644;
  let backup: string | undefined;
  if (existsSync(path)) {
    backup = `${path}.bak.ocx-omo-${new Date().toISOString().replace(/[:.]/g, "-")}`;
    copyFileSync(path, backup);
  }
  const temporary = `${path}.${crypto.randomUUID()}.tmp`;
  writeFileSync(temporary, text, { mode });
  renameSync(temporary, path);
  return backup;
}
