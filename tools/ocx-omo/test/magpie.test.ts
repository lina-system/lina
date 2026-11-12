import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { after, describe, test } from "node:test";
import { loadCatalog, type Catalog } from "../src/catalog.js";
import { OcxOmoError } from "../src/errors.js";
import { loadNames, magpieProvider, type Names } from "../src/names.js";
import { buildPlan, OCX_TARGET, type Entry, type Plan, type Policy, type Target } from "../src/plan.js";
import { extractChains } from "../src/upstream.js";

/**
 * lina#587: the same routes through a node's magpie. The fixture catalogs carry the 16 names the routes of a real
 * node's omo.jsonc use, in OCX form (`models.ocx.json`) and, beside them, in a magpie node's form
 * (`models.magpie.json`: `hub/<provider>/<id>`, no `-fast` tiers, both Gemini namespaces as one provider).
 */
const at = (path: string) => fileURLToPath(new URL(path, import.meta.url));
const FIXTURES = at("../../test/fixtures/");
const NAMES_PATH = at("../../../../hermes_cli/worker/proxy_names.json"); // LINA's one table, not a copy
const CLI = at("../src/cli.js");
const OMO_TASK = join(FIXTURES, "omo-task-5.1.69.js");
const MODELS = join(FIXTURES, "models.magpie.json");

const policy = JSON.parse(readFileSync(at("../../policy.default.json"), "utf8")) as Policy;
const chains = extractChains(readFileSync(OMO_TASK, "utf8"));
const names = loadNames(NAMES_PATH);
const ocxCatalog = loadCatalog(join(FIXTURES, "models.ocx.json"), "opencodex");
const magpieCatalog = loadCatalog(MODELS, "magpie");
const magpie = (remoteId = "hub", table: Names = names): Target => ({ kind: "magpie", remoteId, names: table });
const ocxPlan = buildPlan(chains, policy, ocxCatalog);
const magpiePlan = buildPlan(chains, policy, magpieCatalog, magpie());
const references = (plan: Plan) => Object.values(plan.routes).flatMap((route) => route.models);

/** An OCX route entry under its magpie name, read through the table independently of the planner. */
function asMagpie(entry: Entry): Entry {
  const id = entry.model.slice("opencodex/".length).replace(/--fast$/, "-fast");
  const slash = id.indexOf("/");
  const [namespace, model] = slash < 0 ? ["", id] : [id.slice(0, slash), id.slice(slash + 1)];
  return { ...entry, model: `magpie/hub/${magpieProvider(names, namespace, model)}/${model}` };
}

describe("magpie plan from the fixture catalog", () => {
  test("the OCX plan is the one without a target: 14 routes, 57 references, all opencodex", () => {
    assert.deepEqual(buildPlan(chains, policy, ocxCatalog, OCX_TARGET), ocxPlan);
    assert.equal(Object.keys(ocxPlan.routes).length, 14);
    assert.equal(references(ocxPlan).length, 57);
    assert.equal(new Set(references(ocxPlan).map((entry) => entry.model)).size, 16);
    assert.ok(references(ocxPlan).every((entry) => entry.model.startsWith("opencodex/")));
  });

  test("writes the same 14 routes as magpie/<remote_id>/<provider>/<id> names from the catalog", () => {
    assert.deepEqual(Object.keys(magpiePlan.routes), Object.keys(ocxPlan.routes));
    for (const { model } of references(magpiePlan)) {
      assert.match(model, /^magpie\/hub\/(claude|codex|antigravity|grok)\/[^/]+$/);
      assert.ok(magpieCatalog.has(model.slice("magpie/".length)), model);
    }
    assert.deepEqual(magpiePlan.warnings, ocxPlan.warnings);
  });

  test("keeps omo's order and strength position by position, minus what magpie lacks", () => {
    for (const [path, route] of Object.entries(magpiePlan.routes)) {
      const seen = new Set<string>();
      const expected = (ocxPlan.routes[path]?.models ?? []).map(asMagpie).filter(({ model }) => {
        const keep = magpieCatalog.has(model.slice("magpie/".length)) && !seen.has(model);
        seen.add(model);
        return keep;
      });
      assert.deepEqual(route.models, expected, path);
    }
  });

  test("writes Gemini once per chain, the first namespace's place kept", () => {
    const visual = magpiePlan.routes["categories.visual-engineering"]?.models ?? [];
    assert.deepEqual(visual.slice(0, 2), [
      { model: "magpie/hub/antigravity/gemini-4-argon", reasoning: "low" },
      { model: "magpie/hub/claude/claude-fable-5-2", reasoning: "max" },
    ]);
    for (const route of Object.values(magpiePlan.routes)) {
      const models = route.models.map((entry) => entry.model);
      assert.equal(new Set(models).size, models.length);
    }
  });

  test("skips the -fast tiers and lists them as not routed, extras included", () => {
    const quick = magpiePlan.routes["categories.quick"];
    assert.deepEqual(quick?.models.map((entry) => entry.model), [
      "magpie/hub/claude/claude-haiku-5-5",
      "magpie/hub/antigravity/gemini-3.8-flash",
      "magpie/hub/codex/gpt-6-luna",
    ]);
    assert.ok(quick?.skipped.includes("gpt-6-luna-fast")); // omo's own chain
    assert.ok(quick?.skipped.includes("grok-4.7-fast")); // a policy extra
    assert.ok(magpiePlan.routes["categories.deep-low"]?.skipped.includes("gpt-6.1-terra-fast"));
    assert.ok(!references(magpiePlan).some((entry) => entry.model.endsWith("-fast")));
  });

  test("skips a model the catalog does not carry, as OCX does", () => {
    const without: Catalog = new Map([...magpieCatalog].filter(([id]) => id !== "hub/claude/claude-opus-5-5"));
    const plan = buildPlan(chains, policy, without, magpie());
    const ultrabrain = plan.routes["categories.ultrabrain"];
    assert.deepEqual(ultrabrain?.models.map((entry) => entry.model), [
      "magpie/hub/codex/gpt-6-astra",
      "magpie/hub/codex/gpt-6.1-sol",
    ]);
    assert.ok(ultrabrain?.skipped.includes("claude-opus-5-5"));
  });

  test("routes only to the named remote, and maps a namespace only through the table", () => {
    const elsewhere = buildPlan(chains, policy, magpieCatalog, magpie("other"));
    assert.equal(references(elsewhere).length, 0);
    assert.ok(elsewhere.warnings.includes("categories.quick: no model is available in the catalog"));
    const noGrok = buildPlan(chains, policy, magpieCatalog, magpie("hub", { ...names, namespaces: { anthropic: "claude" } }));
    assert.ok(!references(noGrok).some((entry) => /grok|antigravity/.test(entry.model)));
  });
});

/** lina#623: the catalog of a node whose own magpie holds the models, listed plainly (`<provider>/<id>`). */
const plainCatalog: Catalog = new Map([...magpieCatalog].map(([id, model]) => {
  assert.ok(id.startsWith("hub/") && model.id === id, id);
  return [id.slice("hub/".length), { ...model, id: id.slice("hub/".length) }];
}));
const unprefixed = (plan: Plan): Plan => JSON.parse(JSON.stringify(plan).replaceAll('"magpie/hub/', '"magpie/'));

describe("magpie plan with an empty remote id (the node's own magpie)", () => {
  test("writes magpie/<provider>/<id> from a plain catalog, route by route as through the hub's prefix", () => {
    const plan = buildPlan(chains, policy, plainCatalog, magpie(""));
    assert.deepEqual(plan, unprefixed(magpiePlan));
    assert.ok(references(plan).length > 0);
    for (const { model } of references(plan)) assert.match(model, /^magpie\/(claude|codex|antigravity|grok)\/[^/]+$/);
  });

  test("each form reads only its own catalog", () => {
    assert.equal(references(buildPlan(chains, policy, magpieCatalog, magpie(""))).length, 0);
    assert.equal(references(buildPlan(chains, policy, plainCatalog, magpie("hub"))).length, 0);
  });

  describe("cli", () => {
    const home = mkdtempSync(join(tmpdir(), "ocx-omo-local-"));
    after(() => rmSync(home, { recursive: true, force: true }));
    const models = join(home, "models.json");
    writeFileSync(models, JSON.stringify({ providers: { magpie: { models: [...plainCatalog.values()] } } }));
    const run = (...args: string[]) =>
      spawnSync(process.execPath, [CLI, "plan", "--home", home, "--omo-task", OMO_TASK, "--models", models,
        "--proxy", "magpie", "--names", NAMES_PATH, ...args], { encoding: "utf8" });

    test("an empty --remote-id plans the plain names", () => {
      const planned = run("--remote-id", "", "--json");
      assert.equal(planned.status, 0, planned.stderr);
      assert.deepEqual(JSON.parse(planned.stdout).routes, unprefixed(magpiePlan).routes);
      assert.match(run("--remote-id=").stdout, /through magpie \(local\)/);
    });

    test("still refuses a remote id with '/' with exit 2", () => {
      const refused = run("--remote-id", "a/b");
      assert.equal(refused.status, 2);
      assert.match(refused.stderr, /--remote-id/);
    });
  });
});

describe("loadNames", () => {
  const dir = mkdtempSync(join(tmpdir(), "ocx-omo-names-"));
  after(() => rmSync(dir, { recursive: true, force: true }));

  test("refuses a missing file or a table of another shape", () => {
    assert.throws(() => loadNames(join(dir, "none.json")), OcxOmoError);
    writeFileSync(join(dir, "bad.json"), JSON.stringify({ namespaces: { google: 1 }, bare_prefixes: {} }));
    assert.throws(() => loadNames(join(dir, "bad.json")), /bare_prefixes/);
  });
});

describe("cli through magpie and back", () => {
  const home = mkdtempSync(join(tmpdir(), "ocx-omo-home-"));
  after(() => rmSync(home, { recursive: true, force: true }));
  const config = join(home, ".omo", "omo.jsonc");
  const run = (verb: string, ...args: string[]) =>
    spawnSync(process.execPath, [CLI, verb, "--home", home, "--omo-task", OMO_TASK, "--models", MODELS, ...args], {
      encoding: "utf8",
    });
  const viaMagpie = ["--proxy", "magpie", "--remote-id", "hub", "--names", NAMES_PATH];
  const routes = () => JSON.parse(readFileSync(config, "utf8").replace(/\/\/.*$/gm, "")) as Record<string, any>;

  test("apply magpie, then ocx, restores the OCX routes byte for byte and keeps every backup", () => {
    mkdirSync(join(home, ".omo"));
    writeFileSync(config, '{\n  // mine\n  "categories": { "quick": { "model": "x/y", "prompt_append": "stay" } }\n}\n');
    assert.equal(run("check").status, 10); // no baseline yet
    assert.equal(run("apply").status, 0);
    const ocxText = readFileSync(config, "utf8");
    assert.equal(run("check").status, 0);

    assert.equal(run("check", ...viaMagpie).status, 10); // every route moves
    const applied = run("apply", ...viaMagpie);
    assert.equal(applied.status, 0, applied.stderr);
    assert.equal(run("check", ...viaMagpie).status, 0);
    const moved = routes();
    assert.equal(moved["categories"].quick.prompt_append, "stay");
    assert.deepEqual(moved["categories"].quick.models, magpiePlan.routes["categories.quick"]?.models);
    assert.ok(readFileSync(config, "utf8").includes("// mine"));

    assert.equal(run("check").status, 10);
    assert.equal(run("apply").status, 0);
    assert.equal(readFileSync(config, "utf8"), ocxText);
    assert.equal(run("check").status, 0);
    assert.equal(readdirSync(join(home, ".omo")).filter((name) => name.startsWith("omo.jsonc.bak.ocx-omo-")).length, 3);
  });

  test("refuses magpie without the name table, and an unknown proxy, with exit 2", () => {
    assert.equal(run("plan", "--proxy", "magpie").status, 2);
    assert.match(run("plan", "--proxy", "magpie").stderr, /--names/);
    assert.equal(run("plan", "--proxy", "openrouter").status, 2);
  });
});
