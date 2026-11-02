import assert from "node:assert/strict";
import { describe, test } from "node:test";
import type { Catalog, CatalogModel } from "../src/catalog.js";
import { buildPlan, clampReasoning, type Policy } from "../src/plan.js";

const levels = (...supported: string[]) =>
  Object.fromEntries(["low", "medium", "high", "xhigh", "max"].map((l) => [l, supported.includes(l) ? l : null]));
const catalogOf = (...models: CatalogModel[]): Catalog => new Map(models.map((m) => [m.id, m]));
const policy: Policy = {
  provider: "opencodex",
  namespaces: ["", "anthropic", "google-antigravity", "google"],
  exclude: ["gpt-x--fast"],
  extras: {},
};
const models = (chain: { model: string; variant?: string }[], catalog: Catalog, p: Policy = policy) =>
  buildPlan({ categories: { r: chain.map((rung) => ({ providers: [], ...rung })) }, agents: {} }, p, catalog).routes["categories.r"];

describe("buildPlan", () => {
  test("expands a model to every allowed namespace in policy order", () => {
    const route = models(
      [{ model: "gemini-4-argon", variant: "low" }],
      catalogOf({ id: "google/gemini-4-argon" }, { id: "google-antigravity/gemini-4-argon" }, { id: "inferhub/gemini-4-argon" }),
    );
    assert.deepEqual(route?.models.map((e) => e.model), [
      "opencodex/google-antigravity/gemini-4-argon",
      "opencodex/google/gemini-4-argon",
    ]);
  });

  test("maps -fast to --fast and honours exclusions", () => {
    const route = models(
      [{ model: "gpt-x-fast" }, { model: "gpt-y-fast" }],
      catalogOf({ id: "gpt-x--fast" }, { id: "gpt-y--fast" }),
    );
    assert.deepEqual(route?.models, [{ model: "opencodex/gpt-y--fast" }]);
    assert.deepEqual(route?.skipped, ["gpt-x-fast"]);
  });

  test("keeps the first occurrence of a model", () => {
    const route = models(
      [{ model: "gpt-a", variant: "xhigh" }, { model: "gpt-a", variant: "high" }],
      catalogOf({ id: "gpt-a" }),
    );
    assert.deepEqual(route?.models, [{ model: "opencodex/gpt-a", reasoning: "xhigh" }]);
  });

  test("appends extras after the upstream chain and warns on a dead extras route", () => {
    const withExtras = { ...policy, extras: { "categories.r": [{ model: "gpt-b", reasoning: "high" }], "categories.gone": [] } };
    const plan = buildPlan(
      { categories: { r: [{ model: "gpt-a", providers: [] }] }, agents: {} },
      withExtras,
      catalogOf({ id: "gpt-a" }, { id: "gpt-b" }),
    );
    assert.deepEqual(plan.routes["categories.r"]?.models.map((e) => e.model), ["opencodex/gpt-a", "opencodex/gpt-b"]);
    assert.ok(plan.warnings.includes("policy extras name a route omo no longer has: categories.gone"));
  });

  test("warns when a route ends up empty", () => {
    const plan = buildPlan({ categories: { r: [{ model: "nope", providers: [] }] }, agents: {} }, policy, catalogOf({ id: "gpt-a" }));
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
