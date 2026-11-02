import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { OcxOmoError } from "../src/errors.js";
import { extractChains } from "../src/upstream.js";

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
    assert.deepEqual(chains.categories["visual-engineering"]?.map((rung) => rung.model), [
      "gemini-4-argon",
      "claude-opus-5-5",
    ]);
    assert.equal(chains.categories["quick"]?.[0]?.model, "gpt-6-astra");
  });

  test("reads agent chains", () => {
    const chains = extractChains(MINIFIED);
    assert.deepEqual(Object.keys(chains.agents), ["explore", "plan-reviewer"]);
    assert.deepEqual(chains.agents["plan-reviewer"]?.[0], { providers: ["openai"], model: "gpt-6-astra", variant: "xhigh" });
  });

  test("fails loudly when the chains are gone", () => {
    assert.throws(() => extractChains("var nothing=1;"), OcxOmoError);
  });

  test("fails loudly when a constant cannot be resolved", () => {
    const broken = MINIFIED.replace('qa={claudeOpus:"claude-opus-5-5",gptAstra:"gpt-6-astra"}', "qa=make()");
    assert.throws(() => extractChains(broken), /constant qa/);
  });
});
