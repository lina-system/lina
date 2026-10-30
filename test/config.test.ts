import { describe, expect, test } from "bun:test";
import { currentEntries, editRouting } from "../src/config";
import type { Plan } from "../src/plan";

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
  const parsed = Bun.JSONC.parse(edited) as Record<string, any>;

  test("replaces routing keys and keeps the route's other keys", () => {
    expect(parsed["categories"].quick).toEqual({
      prompt_append: "stay",
      models: [{ model: "opencodex/a", reasoning: "low" }],
    });
  });

  test("keeps comments and unrelated settings", () => {
    expect(edited).toContain("// keep this comment");
    expect(parsed["telemetry"]).toEqual({ enabled: true });
  });

  test("creates a missing section and leaves empty routes unwritten", () => {
    expect(parsed["agents"].explore.models).toEqual([{ model: "opencodex/b" }]);
    expect(parsed["categories"].empty).toBeUndefined();
  });

  test("only touches the selected routes", () => {
    const partial = Bun.JSONC.parse(editRouting(ORIGINAL, plan, ["agents.explore"])) as Record<string, any>;
    expect(partial["categories"].quick.model).toBe("old/x");
  });
});

describe("currentEntries", () => {
  test("reads a lone model as one entry", () => {
    const config = { categories: { quick: { model: "m", reasoning: "low" } } };
    expect(currentEntries(config, "categories.quick")).toEqual([{ model: "m", reasoning: "low" }]);
  });

  test("normalises string entries and missing routes", () => {
    const config = { agents: { explore: { models: ["m"] } } };
    expect(currentEntries(config, "agents.explore")).toEqual([{ model: "m" }]);
    expect(currentEntries(config, "agents.nope")).toEqual([]);
  });
});
