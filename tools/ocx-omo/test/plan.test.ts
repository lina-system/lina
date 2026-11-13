import assert from "node:assert/strict";
import { describe, test } from "node:test";
import type { Catalog, CatalogModel } from "../src/catalog.js";
import { buildPlan, clampReasoning, type Policy } from "../src/plan.js";

const levels = (...supported: string[]) =>
  Object.fromEntries(["low", "medium", "high", "xhigh", "max"].map((l) => [l, supported.includes(l) ? l : null]));
const catalogOf = (...models: CatalogModel[]): Catalog => new Map(models.map((m) => [m.id, m]));
const policy: Policy = {
  providers: ["codex", "claude", "antigravity", "grok"],
  extras: {},
};
const models = (chain: { model: string; variant?: string }[], catalog: Catalog, p: Policy = policy) =>
  buildPlan({ categories: { r: chain.map((rung) => ({ providers: [], ...rung })) }, agents: {} }, p, catalog).routes["categories.r"];

describe("buildPlan", () => {
  test("expands a model to every allowed provider in policy order", () => {
    const route = models(
      [{ model: "gemini-4-argon", variant: "low" }],
      catalogOf({ id: "hub/grok/gemini-4-argon" }, { id: "hub/antigravity/gemini-4-argon" }, { id: "hub/inferhub/gemini-4-argon" }),
    );
    assert.deepEqual(route?.models.map((e) => e.model), [
      "magpie/hub/antigravity/gemini-4-argon",
      "magpie/hub/grok/gemini-4-argon",
    ]);
  });

  test("routes catalog ids literally, including available fast models", () => {
    const route = models(
      [{ model: "gpt-x-fast" }, { model: "gpt-y-fast" }],
      catalogOf({ id: "hub/codex/gpt-x--fast" }, { id: "hub/codex/gpt-y-fast" }),
    );
    assert.deepEqual(route?.models, [{ model: "magpie/hub/codex/gpt-y-fast" }]);
    assert.deepEqual(route?.skipped, ["gpt-x-fast"]);
  });

  test("keeps the first occurrence of a model", () => {
    const route = models(
      [{ model: "gpt-a", variant: "xhigh" }, { model: "gpt-a", variant: "high" }],
      catalogOf({ id: "hub/codex/gpt-a" }),
    );
    assert.deepEqual(route?.models, [{ model: "magpie/hub/codex/gpt-a", reasoning: "xhigh" }]);
  });

  test("appends extras after the upstream chain and warns on a dead extras route", () => {
    const withExtras = { ...policy, extras: { "categories.r": [{ model: "gpt-b", reasoning: "high" }], "categories.gone": [] } };
    const plan = buildPlan(
      { categories: { r: [{ model: "gpt-a", providers: [] }] }, agents: {} },
      withExtras,
      catalogOf({ id: "hub/codex/gpt-a" }, { id: "hub/codex/gpt-b" }),
    );
    assert.deepEqual(plan.routes["categories.r"]?.models.map((e) => e.model), ["magpie/hub/codex/gpt-a", "magpie/hub/codex/gpt-b"]);
    assert.ok(plan.warnings.includes("policy extras name a route omo no longer has: categories.gone"));
  });

  test("warns when a route ends up empty", () => {
    const plan = buildPlan({ categories: { r: [{ model: "nope", providers: [] }] }, agents: {} }, policy, catalogOf({ id: "hub/codex/gpt-a" }));
    assert.ok(plan.warnings.includes("categories.r: no model is available in the catalog"));
  });
});

describe("clampReasoning", () => {
  test("uses the nearest supported level below, else above", () => {
    assert.equal(clampReasoning({ id: "m", thinkingLevelMap: levels("low", "high") }, "max"), "high");
    assert.equal(clampReasoning({ id: "m", thinkingLevelMap: levels("low", "high") }, "medium"), "low");
    assert.equal(clampReasoning({ id: "m", thinkingLevelMap: levels("high") }, "low"), "high");
    assert.equal(clampReasoning({ id: "m", thinkingLevelMap: levels("low") }, "off"), "low");
  });

  test("keeps the level when the model declares no map", () => {
    assert.equal(clampReasoning({ id: "m" }, "max"), "max");
  });
});
