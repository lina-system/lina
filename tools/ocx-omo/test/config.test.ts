import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { currentEntries, editRouting } from "../src/config.js";
import { parseJsonc } from "../src/jsonc.js";
import type { Plan } from "../src/plan.js";

const plan: Plan = {
  warnings: [],
  routes: {
    "categories.quick": { models: [{ model: "opencodex/a", reasoning: "low" }], skipped: [] },
    "categories.empty": { models: [], skipped: ["x"] },
    "agents.explore": { models: [{ model: "opencodex/b" }], skipped: [] },
  },
};
const ORIGINAL = `{
  // keep this comment
  "telemetry": { "enabled": true },
  "categories": {
    "quick": { "model": "old/x", "reasoning": "high", "prompt_append": "stay" }
  }
}
`;

describe("editRouting", () => {
  const edited = editRouting(ORIGINAL, plan, Object.keys(plan.routes));
  const parsed = parseJsonc(edited, "edited") as Record<string, any>;

  test("replaces routing keys and keeps the route's other keys", () => {
    assert.deepEqual(parsed["categories"].quick, {
      prompt_append: "stay",
      models: [{ model: "opencodex/a", reasoning: "low" }],
    });
  });

  test("keeps comments and unrelated settings", () => {
    assert.ok(edited.includes("// keep this comment"));
    assert.deepEqual(parsed["telemetry"], { enabled: true });
  });

  test("creates a missing section and leaves empty routes unwritten", () => {
    assert.deepEqual(parsed["agents"].explore.models, [{ model: "opencodex/b" }]);
    assert.equal(parsed["categories"].empty, undefined);
  });

  test("only touches the selected routes", () => {
    const partial = parseJsonc(editRouting(ORIGINAL, plan, ["agents.explore"]), "edited") as Record<string, any>;
    assert.equal(partial["categories"].quick.model, "old/x");
  });
});

describe("currentEntries", () => {
  test("reads a lone model as one entry", () => {
    const config = { categories: { quick: { model: "m", reasoning: "low" } } };
    assert.deepEqual(currentEntries(config, "categories.quick"), [{ model: "m", reasoning: "low" }]);
  });

  test("normalises string entries and missing routes", () => {
    const config = { agents: { explore: { models: ["m"] } } };
    assert.deepEqual(currentEntries(config, "agents.explore"), [{ model: "m" }]);
    assert.deepEqual(currentEntries(config, "agents.nope"), []);
  });
});
