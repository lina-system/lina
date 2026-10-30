import { describe, expect, test } from "bun:test";
import { OcxOmoError } from "../src/errors";
import { extractChains } from "../src/upstream";

const MINIFIED = [
  'var unrelated=1,qa={other:"x"};qa={claudeOpus:"claude-opus-5-5",gptAstra:"gpt-6-astra"};',
  'function w(e){return e.filter(Boolean)}',
  'zz={"visual-engineering":w([{providers:["google"],model:"gemini-4-argon",variant:"low"},',
  '{providers:["anthropic"],model:qa.claudeOpus,variant:"max"}]),',
  'quick:w([{providers:["openai"],model:qa.gptAstra}])};',
  'Ha={explore:[{providers:["kimi"],model:"kimi-x",variant:"off"}],"plan-reviewer":[{providers:["openai"],model:qa.gptAstra,variant:"xhigh"}]};',
].join("");

describe("extractChains", () => {
  test("reads wrapped category chains and resolves minified constants", () => {
    const chains = extractChains(MINIFIED);
    expect(chains.categories["visual-engineering"]?.map((rung) => rung.model)).toEqual([
      "gemini-4-argon",
      "claude-opus-5-5",
    ]);
    expect(chains.categories["quick"]?.[0]?.model).toBe("gpt-6-astra");
  });

  test("reads agent chains", () => {
    const chains = extractChains(MINIFIED);
    expect(Object.keys(chains.agents)).toEqual(["explore", "plan-reviewer"]);
    expect(chains.agents["plan-reviewer"]?.[0]).toEqual({ providers: ["openai"], model: "gpt-6-astra", variant: "xhigh" });
  });

  test("fails loudly when the chains are gone", () => {
    expect(() => extractChains("var nothing=1;")).toThrow(OcxOmoError);
  });

  test("fails loudly when a constant cannot be resolved", () => {
    const broken = MINIFIED.replace('qa={claudeOpus:"claude-opus-5-5",gptAstra:"gpt-6-astra"}', "qa=make()");
    expect(() => extractChains(broken)).toThrow(/constant qa/);
  });
});
