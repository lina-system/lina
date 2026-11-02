import { describe, expect, test } from "bun:test";
import type { Catalog, CatalogModel } from "../src/catalog";
import { buildPlan, clampReasoning, type Policy } from "../src/plan";

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
    expect(route?.models.map((e) => e.model)).toEqual([
      "opencodex/google-antigravity/gemini-4-argon",
      "opencodex/google/gemini-4-argon",
    ]);
  });

  test("maps -fast to --fast and honours exclusions", () => {
    const route = models(
      [{ model: "gpt-x-fast" }, { model: "gpt-y-fast" }],
      catalogOf({ id: "gpt-x--fast" }, { id: "gpt-y--fast" }),
    );
    expect(route?.models).toEqual([{ model: "opencodex/gpt-y--fast" }]);
    expect(route?.skipped).toEqual(["gpt-x-fast"]);
  });

  test("keeps the first occurrence of a model", () => {
    const route = models(
      [{ model: "gpt-a", variant: "xhigh" }, { model: "gpt-a", variant: "high" }],
      catalogOf({ id: "gpt-a" }),
    );
    expect(route?.models).toEqual([{ model: "opencodex/gpt-a", reasoning: "xhigh" }]);
  });

  test("appends extras after the upstream chain and warns on a dead extras route", () => {
    const withExtras = { ...policy, extras: { "categories.r": [{ model: "gpt-b", reasoning: "high" }], "categories.gone": [] } };
    const plan = buildPlan(
      { categories: { r: [{ model: "gpt-a", providers: [] }] }, agents: {} },
      withExtras,
      catalogOf({ id: "gpt-a" }, { id: "gpt-b" }),
    );
    expect(plan.routes["categories.r"]?.models.map((e) => e.model)).toEqual(["opencodex/gpt-a", "opencodex/gpt-b"]);
    expect(plan.warnings).toContain("policy extras name a route omo no longer has: categories.gone");
  });

  test("warns when a route ends up empty", () => {
    const plan = buildPlan({ categories: { r: [{ model: "nope", providers: [] }] }, agents: {} }, policy, catalogOf({ id: "gpt-a" }));
    expect(plan.warnings).toContain("categories.r: no model is available in the catalog");
  });
});

describe("clampReasoning", () => {
  test("uses the nearest supported level below, else above", () => {
    expect(clampReasoning({ id: "m", thinkingLevelMap: levels("low", "high") }, "max")).toBe("high");
    expect(clampReasoning({ id: "m", thinkingLevelMap: levels("low", "high") }, "medium")).toBe("low");
    expect(clampReasoning({ id: "m", thinkingLevelMap: levels("high") }, "low")).toBe("high");
    expect(clampReasoning({ id: "m", thinkingLevelMap: levels("low") }, "off")).toBe("low");
  });

  test("keeps the level when the model declares no map", () => {
    expect(clampReasoning({ id: "m" }, "max")).toBe("max");
  });
});
