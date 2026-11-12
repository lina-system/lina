#!/usr/bin/env node
import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { loadCatalog } from "./catalog.js";
import { currentEntries, editRouting, nativeOverrideWarnings, readConfig, writeConfig } from "./config.js";
import { OcxOmoError } from "./errors.js";
import { parseJsonc } from "./jsonc.js";
import { loadNames } from "./names.js";
import {
  buildPlan, formatEntries, MAGPIE_PROVIDER, OCX_TARGET, type Entry, type Plan, type Policy, type Target,
} from "./plan.js";
import { readState, writeState, type State } from "./state.js";
import { loadUpstream, type Chains, type Rung } from "./upstream.js";

/** The bundled policy; the build keeps src/ and policy.default.json one level apart under dist/. */
const DEFAULT_POLICY = new URL("../../policy.default.json", import.meta.url);

const EXIT_PENDING = 10;
const EXIT_ERROR = 2;
const USAGE = `Usage: ocx-omo <plan|apply|check> [options]

  plan    Show the routing proposed for the installed omo, and how it differs from omo.jsonc.
  apply   Write that routing into omo.jsonc (with a backup) and record it as the baseline.
  check   After an omo update: compare against the baseline. Exit 0 unchanged, 10 changes pending.

Options:
  --only <paths>     apply only: comma-separated routes, e.g. categories.quick,agents.explore
  --home <dir>       Base for the default paths below (default: your home directory)
  --omo-task <path>  omo-task.js of the omo to read (default: auto-detected)
  --models <path>    Catalog (default: <home>/.omo/agent/models.json)
  --config <path>    omo config (default: <home>/.omo/omo.jsonc)
  --policy <path>    Policy (default: <home>/.omo/ocx-omo/policy.json, else the bundled policy)
  --state <path>     Baseline (default: <home>/.omo/ocx-omo/state.json)
  --proxy <kind>     ocx (default): routes through OCX's opencodex provider; magpie: through the node's magpie
  --remote-id <id>   magpie: the id of the magpie link whose models are routed to (default: hub)
  --names <path>     magpie: the name table from OCX namespaces to magpie providers (required with magpie)
  --json             Machine-readable output
`;

const OPTIONS = {
  only: { type: "string" },
  home: { type: "string" },
  "omo-task": { type: "string" },
  models: { type: "string" },
  config: { type: "string" },
  policy: { type: "string" },
  state: { type: "string" },
  proxy: { type: "string" },
  "remote-id": { type: "string" },
  names: { type: "string" },
  json: { type: "boolean" },
  help: { type: "boolean", short: "h" },
} as const;

type Context = {
  policy: Policy;
  plan: Plan;
  /** The provider prefix the output leaves out: the policy's for OCX, `magpie` for magpie. */
  provider: string;
  target: Target;
  chains: Chains;
  omoVersion: string;
  configPath: string;
  statePath: string;
  json: boolean;
};

function targetOf(values: ReturnType<typeof parse>["values"]): Target {
  const kind = values.proxy ?? "ocx";
  if (kind === "ocx") return OCX_TARGET;
  if (kind !== "magpie") throw new OcxOmoError(`--proxy: ocx or magpie, not ${kind}`);
  const remoteId = values["remote-id"] ?? "hub";
  if (!remoteId || remoteId.includes("/")) throw new OcxOmoError(`--remote-id: a name without '/', not ${remoteId}`);
  if (!values.names) throw new OcxOmoError("--proxy magpie needs --names <path> (the name table)");
  return { kind, remoteId, names: loadNames(values.names) };
}

function load(values: ReturnType<typeof parse>["values"]): Context {
  const home = values.home ?? homedir();
  const policyPath = values.policy ?? join(home, ".omo/ocx-omo/policy.json");
  if (values.policy && !existsSync(values.policy)) throw new OcxOmoError(`policy not found: ${values.policy}`);
  const policy = existsSync(policyPath)
    ? (parseJsonc(readFileSync(policyPath, "utf8"), policyPath) as Policy)
    : (JSON.parse(readFileSync(DEFAULT_POLICY, "utf8")) as Policy);
  const target = targetOf(values);
  const upstream = loadUpstream(values["omo-task"]);
  const models = values.models ?? join(home, ".omo/agent/models.json");
  const catalog = target.kind === "magpie"
    ? loadCatalog(models, MAGPIE_PROVIDER, "run magpie's connection command for omo")
    : loadCatalog(models, policy.provider);
  return {
    policy,
    plan: buildPlan(upstream.chains, policy, catalog, target),
    provider: target.kind === "magpie" ? MAGPIE_PROVIDER : policy.provider,
    target,
    chains: upstream.chains,
    omoVersion: upstream.version,
    configPath: values.config ?? join(home, ".omo/omo.jsonc"),
    statePath: values.state ?? join(home, ".omo/ocx-omo/state.json"),
    json: values.json ?? false,
  };
}

function parse(argv: string[]) {
  return parseArgs({ args: argv, options: OPTIONS, allowPositionals: true });
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

function chainOf(chains: Chains, path: string): Rung[] {
  const dot = path.indexOf(".");
  const scope = path.slice(0, dot) as keyof Chains;
  return chains[scope][path.slice(dot + 1)] ?? [];
}

function printRoutes(context: Context, config: Record<string, unknown>): void {
  const { plan, provider } = context;
  for (const [path, route] of Object.entries(plan.routes)) {
    const current = currentEntries(config, path);
    const mark = same(current, route.models) ? " " : "*";
    console.log(`${mark} ${path}: ${formatEntries(route.models, provider)}`);
    if (mark === "*" && current.length) console.log(`    now: ${formatEntries(current, provider)}`);
    if (route.skipped.length) console.log(`    not routed: ${route.skipped.join(", ")}`);
  }
}

function runPlan(context: Context): number {
  const { parsed } = readConfig(context.configPath);
  const warnings = [...context.plan.warnings, ...nativeOverrideWarnings(parsed)];
  if (context.json) {
    console.log(JSON.stringify({ omoVersion: context.omoVersion, routes: context.plan.routes, warnings }, null, 2));
    return 0;
  }
  const through = context.target.kind === "magpie" ? ` through magpie (${context.target.remoteId})` : "";
  console.log(`omo ${context.omoVersion}: proposed routing${through} (* differs from ${context.configPath})`);
  printRoutes(context, parsed);
  for (const warning of warnings) console.log(`warning: ${warning}`);
  return 0;
}

function runApply(context: Context, only: string | undefined): number {
  const { plan } = context;
  const all = Object.keys(plan.routes);
  const selected = only ? only.split(",").map((path) => path.trim()) : all;
  const unknown = selected.filter((path) => !(path in plan.routes));
  if (unknown.length) throw new OcxOmoError(`unknown route: ${unknown.join(", ")}`);
  const writable = selected.filter((path) => plan.routes[path]?.models.length);
  const config = readConfig(context.configPath);
  for (const warning of [...plan.warnings, ...nativeOverrideWarnings(config.parsed)]) console.log(`warning: ${warning}`);
  const backup = writeConfig(context.configPath, editRouting(config.text, plan, writable));
  const previous = readState(context.statePath);
  const state: State = {
    omoVersion: context.omoVersion,
    appliedAt: new Date().toISOString(),
    upstream: { ...previous?.upstream },
    routing: { ...previous?.routing },
  };
  for (const path of writable) {
    state.upstream[path] = chainOf(context.chains, path);
    state.routing[path] = plan.routes[path]?.models ?? [];
  }
  writeState(context.statePath, state);
  console.log(`applied ${writable.length} routes to ${context.configPath}`);
  if (backup) console.log(`backup: ${backup}`);
  return 0;
}

function runCheck(context: Context): number {
  const { plan, provider } = context;
  const state = readState(context.statePath);
  if (!state) {
    console.log("no baseline yet: review `ocx-omo plan`, then run `ocx-omo apply`");
    return EXIT_PENDING;
  }
  const paths = [...new Set([...Object.keys(plan.routes), ...Object.keys(state.routing)])];
  const changed = paths.filter((path) => {
    const routed = !same(state.routing[path] ?? [], plan.routes[path]?.models ?? []);
    return routed || !same(state.upstream[path] ?? [], chainOf(context.chains, path));
  });
  const { parsed } = readConfig(context.configPath);
  const edited = paths.filter(
    (path) => state.routing[path] && !same(currentEntries(parsed, path), state.routing[path]),
  );
  if (context.json) {
    console.log(JSON.stringify({ omoVersion: context.omoVersion, baselineVersion: state.omoVersion, changed, edited }, null, 2));
    return changed.length ? EXIT_PENDING : 0;
  }
  const versions = state.omoVersion === context.omoVersion ? context.omoVersion : `${state.omoVersion} -> ${context.omoVersion}`;
  for (const path of edited) console.log(`note: ${path} was edited by hand since the last apply`);
  for (const warning of plan.warnings) console.log(`warning: ${warning}`);
  if (!changed.length) {
    console.log(`no change: routing still matches omo ${versions}`);
    return 0;
  }
  console.log(`omo ${versions}: ${changed.length} route(s) changed`);
  for (const path of changed) {
    const before = (state.upstream[path] ?? []).map((rung) => rung.model).join(", ");
    const after = chainOf(context.chains, path).map((rung) => rung.model).join(", ");
    console.log(`${path}`);
    if (before !== after) {
      console.log(`  upstream before: ${before || "(none)"}`);
      console.log(`  upstream after:  ${after || "(removed)"}`);
    }
    const entries: Entry[] = plan.routes[path]?.models ?? [];
    console.log(`  routing before:  ${formatEntries(state.routing[path] ?? [], provider) || "(none)"}`);
    console.log(`  routing after:   ${formatEntries(entries, provider) || "(none)"}`);
  }
  console.log(`apply with: ocx-omo apply --only ${changed.join(",")}`);
  return EXIT_PENDING;
}

function main(argv: string[]): number {
  const { values, positionals } = parse(argv);
  const command = positionals[0];
  if (values.help || !command) {
    console.log(USAGE);
    return values.help ? 0 : EXIT_ERROR;
  }
  if (command !== "plan" && command !== "apply" && command !== "check")
    throw new OcxOmoError(`unknown command: ${command}\n${USAGE}`);
  const context = load(values);
  if (command === "plan") return runPlan(context);
  if (command === "apply") return runApply(context, values.only);
  return runCheck(context);
}

try {
  process.exitCode = main(process.argv.slice(2));
} catch (error) {
  const badArgs = error instanceof TypeError && "code" in error && String(error.code).startsWith("ERR_PARSE_ARGS");
  if (!(error instanceof OcxOmoError) && !badArgs) throw error;
  console.error(`ocx-omo: ${(error as Error).message}`);
  process.exitCode = EXIT_ERROR;
}
