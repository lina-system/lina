import type { Catalog, CatalogModel } from "./catalog.js";
import { magpieProvider, type Names } from "./names.js";
import type { Chains, Rung } from "./upstream.js";

export type Extra = { model: string; reasoning?: string };
export type Policy = {
  provider: string;
  /** opencodex namespaces tried for every model, in order; "" is the bare OpenAI namespace. */
  namespaces: string[];
  /** Exact opencodex ids never routed to. */
  exclude: string[];
  /** Models appended after the upstream chain, by route path. */
  extras: Record<string, Extra[]>;
};
export type Entry = { model: string; reasoning?: string };
export type RoutePlan = { models: Entry[]; skipped: string[] };
export type Plan = { routes: Record<string, RoutePlan>; warnings: string[] };
/**
 * The proxy the routes go through (lina#587): OCX's own provider, or a node's magpie, whose catalog names a model
 * `<remoteId>/<provider>/<id>` and whose provider is the OCX namespace's per the name table.
 */
export type Target = { kind: "ocx" } | { kind: "magpie"; remoteId: string; names: Names };
export const OCX_TARGET: Target = { kind: "ocx" };
export const MAGPIE_PROVIDER = "magpie";
type Resolved = { model: string; entry: CatalogModel };

const LEVELS = ["minimal", "low", "medium", "high", "xhigh", "max"];
const FAST_SUFFIX = "-fast";

/** Upstream writes a priority tier as `x-fast`; opencodex publishes it as `x--fast`. */
const ocxIds = (base: string, name: string) =>
  name.endsWith(FAST_SUFFIX) ? [base, `${base.slice(0, -FAST_SUFFIX.length)}--fast`] : [base];

function resolveIds(name: string, policy: Policy, catalog: Catalog): Resolved[] {
  const resolved: Resolved[] = [];
  for (const namespace of policy.namespaces) {
    const base = namespace ? `${namespace}/${name}` : name;
    const id = ocxIds(base, name).find((candidate) => catalog.has(candidate));
    const model = id === undefined ? undefined : catalog.get(id);
    if (model && !policy.exclude.includes(model.id)) resolved.push({ model: `${policy.provider}/${model.id}`, entry: model });
  }
  return resolved;
}

/**
 * The same walk over the policy's namespaces, each mapped to its magpie provider: a namespace with no provider, an
 * excluded OCX id, or a model the magpie catalog does not carry (the `-fast` tiers) gives no candidate. Two namespaces
 * of one provider give the same name; the caller keeps the first.
 */
function resolveMagpie(name: string, policy: Policy, catalog: Catalog, remoteId: string, names: Names): Resolved[] {
  const resolved: Resolved[] = [];
  for (const namespace of policy.namespaces) {
    const base = namespace ? `${namespace}/${name}` : name;
    if (ocxIds(base, name).some((id) => policy.exclude.includes(id))) continue;
    const provider = magpieProvider(names, namespace, name);
    const model = provider === undefined ? undefined : catalog.get(`${remoteId}/${provider}/${name}`);
    if (model) resolved.push({ model: `${MAGPIE_PROVIDER}/${model.id}`, entry: model });
  }
  return resolved;
}

/** The requested level, else the nearest supported level below it, else the nearest above. */
export function clampReasoning(model: CatalogModel, requested: string | undefined): string | undefined {
  const map = model.thinkingLevelMap;
  if (requested === undefined || map === undefined) return requested;
  const supported = (level: string) => typeof map[level] === "string";
  if (supported(requested)) return requested;
  const index = LEVELS.indexOf(requested);
  const below = LEVELS.slice(0, Math.max(index, 0)).reverse();
  const above = LEVELS.slice(index + 1);
  return [...below, ...above].find(supported);
}

function planRoute(path: string, chain: Rung[], policy: Policy, catalog: Catalog, target: Target): RoutePlan {
  const wanted: Extra[] = [
    ...chain.map((rung) => ({ model: rung.model, ...(rung.variant ? { reasoning: rung.variant } : {}) })),
    ...(policy.extras[path] ?? []),
  ];
  const models: Entry[] = [];
  const skipped: string[] = [];
  const seen = new Set<string>();
  for (const { model: name, reasoning } of wanted) {
    const resolved = target.kind === "magpie"
      ? resolveMagpie(name, policy, catalog, target.remoteId, target.names)
      : resolveIds(name, policy, catalog);
    if (resolved.length === 0 && !skipped.includes(name)) skipped.push(name);
    for (const { model, entry } of resolved) {
      if (seen.has(model)) continue;
      seen.add(model);
      const level = clampReasoning(entry, reasoning);
      models.push({ model, ...(level === undefined ? {} : { reasoning: level }) });
    }
  }
  return { models, skipped };
}

export function buildPlan(chains: Chains, policy: Policy, catalog: Catalog, target: Target = OCX_TARGET): Plan {
  const routes: Record<string, RoutePlan> = {};
  for (const scope of ["categories", "agents"] as const) {
    for (const [name, chain] of Object.entries(chains[scope])) {
      const path = `${scope}.${name}`;
      routes[path] = planRoute(path, chain, policy, catalog, target);
    }
  }
  const warnings = Object.keys(policy.extras)
    .filter((path) => !(path in routes))
    .map((path) => `policy extras name a route omo no longer has: ${path}`);
  for (const [path, route] of Object.entries(routes)) {
    if (route.models.length === 0) warnings.push(`${path}: no model is available in the catalog`);
  }
  return { routes, warnings };
}

export const formatEntries = (entries: Entry[], provider: string) =>
  entries
    .map(({ model, reasoning }) => {
      const short = model.startsWith(`${provider}/`) ? model.slice(provider.length + 1) : model;
      return reasoning ? `${short}:${reasoning}` : short;
    })
    .join(" -> ");
