import { existsSync, readFileSync } from "node:fs";
import { OcxOmoError } from "./errors.js";

/**
 * LINA's one name table (`hermes_cli/worker/proxy_names.json`, lina#585), read where LINA passes it (`--names`):
 * an OCX upstream namespace, or the start of a bare OCX id, to the magpie provider that carries the same model.
 */
export type Names = { namespaces: Record<string, string>; bare_prefixes: Record<string, string> };

const isTable = (value: unknown): value is Record<string, string> =>
  typeof value === "object" && value !== null && !Array.isArray(value) &&
  Object.values(value).every((to) => typeof to === "string" && to.length > 0);

export function loadNames(path: string): Names {
  if (!existsSync(path)) throw new OcxOmoError(`name table not found: ${path}`);
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(path, "utf8").replace(/^\uFEFF/, ""));
  } catch (error) {
    throw new OcxOmoError(`${path}: ${(error as Error).message}`);
  }
  const doc = parsed as Partial<Names> | null;
  if (!isTable(doc?.namespaces) || !isTable(doc?.bare_prefixes))
    throw new OcxOmoError(`${path}: needs the objects "namespaces" and "bare_prefixes" of provider names`);
  return { namespaces: doc.namespaces, bare_prefixes: doc.bare_prefixes };
}

/** The magpie provider of an OCX namespace ("" is OCX's bare namespace, read by the id's start); else undefined. */
export function magpieProvider(names: Names, namespace: string, id: string): string | undefined {
  if (namespace) return Object.hasOwn(names.namespaces, namespace) ? names.namespaces[namespace] : undefined;
  return Object.entries(names.bare_prefixes).find(([prefix]) => id.startsWith(prefix))?.[1];
}
