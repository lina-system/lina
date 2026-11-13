import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { after, describe, test } from "node:test";
import { loadCatalog, type Catalog } from "../src/catalog.js";
import { OcxOmoError } from "../src/errors.js";
import { currentEntries, readConfig } from "../src/config.js";
import { buildPlan, type Plan, type Policy } from "../src/plan.js";
import { extractChains } from "../src/upstream.js";

/**
 * lina#587: routes through a node's magpie, using catalog ids `hub/<provider>/<id>`.
 */
const at = (path: string) => fileURLToPath(new URL(path, import.meta.url));
const FIXTURES = at("../../test/fixtures/");
const CLI = at("../src/cli.js");
const OMO_TASK = join(FIXTURES, "omo-task-5.1.69.js");
const MODELS = join(FIXTURES, "models.magpie.json");

const policy = JSON.parse(readFileSync(at("../../policy.default.json"), "utf8")) as Policy;
const chains = extractChains(readFileSync(OMO_TASK, "utf8"));
const magpieCatalog = loadCatalog(MODELS);
const magpiePlan = buildPlan(chains, policy, magpieCatalog);
const references = (plan: Plan) => Object.values(plan.routes).flatMap((route) => route.models);

describe("magpie plan from the fixture catalog", () => {
  test("writes the same 14 routes as magpie/<remote_id>/<provider>/<id> names from the catalog", () => {
    assert.equal(Object.keys(magpiePlan.routes).length, 14);
    for (const { model } of references(magpiePlan)) {
      assert.match(model, /^magpie\/hub\/(claude|codex|antigravity|grok)\/[^/]+$/);
      assert.ok(magpieCatalog.has(model.slice("magpie/".length)), model);
    }
    assert.deepEqual(magpiePlan.warnings, []);
  });

  test("writes Gemini once per chain, keeping upstream order and reasoning", () => {
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

  test("skips a model the catalog does not carry", () => {
    const without: Catalog = new Map([...magpieCatalog].filter(([id]) => id !== "hub/claude/claude-opus-5-5"));
    const plan = buildPlan(chains, policy, without);
    const ultrabrain = plan.routes["categories.ultrabrain"];
    assert.deepEqual(ultrabrain?.models.map((entry) => entry.model), [
      "magpie/hub/codex/gpt-6-astra",
      "magpie/hub/codex/gpt-6.1-sol",
    ]);
    assert.ok(ultrabrain?.skipped.includes("claude-opus-5-5"));
  });

  test("routes only to the named remote and the policy's providers", () => {
    const elsewhere = buildPlan(chains, policy, magpieCatalog, "other");
    assert.equal(references(elsewhere).length, 0);
    assert.ok(elsewhere.warnings.includes("categories.quick: no model is available in the catalog"));
    const noGrok = buildPlan(chains, { ...policy, providers: ["codex", "claude"] }, magpieCatalog);
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
    const plan = buildPlan(chains, policy, plainCatalog, "");
    assert.deepEqual(plan, unprefixed(magpiePlan));
    assert.ok(references(plan).length > 0);
    for (const { model } of references(plan)) assert.match(model, /^magpie\/(claude|codex|antigravity|grok)\/[^/]+$/);
  });

  test("each form reads only its own catalog", () => {
    assert.equal(references(buildPlan(chains, policy, magpieCatalog, "")).length, 0);
    assert.equal(references(buildPlan(chains, policy, plainCatalog, "hub")).length, 0);
  });

  describe("cli", () => {
    const home = mkdtempSync(join(tmpdir(), "ocx-omo-local-"));
    after(() => rmSync(home, { recursive: true, force: true }));
    const models = join(home, "models.json");
    writeFileSync(models, JSON.stringify({ providers: { magpie: { models: [...plainCatalog.values()] } } }));
    const run = (...args: string[]) =>
      spawnSync(process.execPath, [CLI, "plan", "--home", home, "--omo-task", OMO_TASK, "--models", models,
        ...args], { encoding: "utf8" });

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

describe("loadCatalog", () => {
  const dir = mkdtempSync(join(tmpdir(), "ocx-omo-catalog-"));
  after(() => rmSync(dir, { recursive: true, force: true }));

  test("refuses a missing file or a catalog without magpie models", () => {
    assert.throws(() => loadCatalog(join(dir, "none.json")), OcxOmoError);
    writeFileSync(join(dir, "bad.json"), JSON.stringify({ providers: { other: { models: [{ id: "hub/codex/gpt-x" }] } } }));
    assert.throws(() => loadCatalog(join(dir, "bad.json")), /has no magpie models; run magpie's connection command for omo/);
  });
});

describe("cli through magpie", () => {
  const home = mkdtempSync(join(tmpdir(), "ocx-omo-home-"));
  after(() => rmSync(home, { recursive: true, force: true }));
  const config = join(home, ".omo", "omo.jsonc");
  const run = (verb: string, ...args: string[]) =>
    spawnSync(process.execPath, [CLI, verb, "--home", home, "--omo-task", OMO_TASK, "--models", MODELS, ...args], {
      encoding: "utf8",
    });
  test("apply records the baseline and keeps comments, settings and backups", () => {
    mkdirSync(join(home, ".omo"));
    writeFileSync(config, '{\n  // mine\n  "categories": { "quick": { "model": "x/y", "prompt_append": "stay" } }\n}\n');
    assert.equal(run("check").status, 10); // no baseline yet
    const applied = run("apply");
    assert.equal(applied.status, 0, applied.stderr);
    assert.equal(run("check").status, 0);
    const moved = readConfig(config);
    assert.deepEqual(currentEntries(moved.parsed, "categories.quick"), magpiePlan.routes["categories.quick"]?.models);
    assert.ok(moved.text.includes('"prompt_append": "stay"'));
    assert.ok(readFileSync(config, "utf8").includes("// mine"));

    assert.equal(run("check", "--remote-id", "other").status, 10);
    assert.equal(readdirSync(join(home, ".omo")).filter((name) => name.startsWith("omo.jsonc.bak.ocx-omo-")).length, 1);
  });

  test("reads a user policy at the existing path and honours --config with --only", () => {
    const customHome = mkdtempSync(join(tmpdir(), "ocx-omo-policy-"));
    try {
      const policyDir = join(customHome, ".omo", "ocx-omo");
      mkdirSync(policyDir, { recursive: true });
      writeFileSync(join(policyDir, "policy.json"), JSON.stringify({ providers: ["claude"], extras: {} }));
      const customConfig = join(customHome, "custom.jsonc");
      const applied = run("apply", "--home", customHome, "--config", customConfig, "--only", "categories.quick");
      assert.equal(applied.status, 0, applied.stderr);
      const parsed = readConfig(customConfig).parsed;
      assert.deepEqual(currentEntries(parsed, "categories.quick"), [
        { model: "magpie/hub/claude/claude-haiku-5-5", reasoning: "medium" },
      ]);
      assert.deepEqual(currentEntries(parsed, "categories.architect"), []);
      assert.equal(readFileSync(join(policyDir, "state.json"), "utf8").includes("categories.quick"), true);
    } finally {
      rmSync(customHome, { recursive: true, force: true });
    }
  });

  test("refuses a persisted OCX-era policy with a message naming the file", () => {
    const customHome = mkdtempSync(join(tmpdir(), "ocx-omo-oldpolicy-"));
    try {
      const policyDir = join(customHome, ".omo", "ocx-omo");
      mkdirSync(policyDir, { recursive: true });
      writeFileSync(join(policyDir, "policy.json"), JSON.stringify({ provider: "opencodex", namespaces: ["", "claude"], exclude: [], extras: {} }));
      const refused = run("plan", "--home", customHome);
      assert.notEqual(refused.status, 0);
      assert.match(refused.stderr, /policy\.json: no "providers" list/);
    } finally {
      rmSync(customHome, { recursive: true, force: true });
    }
  });

  for (const flag of ["--proxy", "--names"]) {
    test(`refuses removed option ${flag} as unknown with exit 2`, () => {
      const refused = run("plan", flag, "unused");
      assert.equal(refused.status, 2);
      assert.match(refused.stderr, /Unknown option/);
    });
  }
});
