import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, test } from "node:test";
import { OcxOmoError } from "../src/errors.js";
import { escapeRegExp, extractChains } from "../src/upstream.js";

const MINIFIED = [
  'var unrelated=1,qa={other:"x"};qa={claudeOpus:"claude-opus-5-5",gptAstra:"gpt-6-astra"};',
  'function w(e){return e.filter(Boolean)}',
  'zz={"visual-engineering":w([{providers:["google"],model:"gemini-4-argon",variant:"low"},',
  '{providers:["anthropic"],model:qa.claudeOpus,variant:"max"}]),',
  'quick:w([{providers:["openai"],model:qa.gptAstra}])};',
  'Ha={explore:[{providers:["kimi"],model:"kimi-x",variant:"off"}],"plan-reviewer":[{providers:["openai"],model:qa.gptAstra,variant:"xhigh"}]};',
].join("");

/** Excerpts of omo-ai's plugin/extensions/omo-task.js: the chains and the constants they read. */
const fixture = (version: string) =>
  readFileSync(new URL(`../../test/fixtures/omo-task-${version}.js`, import.meta.url), "utf8");
const CATEGORIES = [
  "visual-engineering", "architect", "ultrabrain", "deep-low", "deep-high",
  "artistry", "quick", "unspecified-low", "unspecified-high", "writing",
];
const AGENTS = ["explore", "librarian", "plan-consultant", "plan-reviewer"];

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

  test("reads omo 5.1.66, whose category chains are wrapped in a helper", () => {
    const chains = extractChains(fixture("5.1.66"));
    assert.deepEqual(Object.keys(chains.categories), CATEGORIES);
    assert.deepEqual(Object.keys(chains.agents), AGENTS);
    assert.deepEqual(chains.categories["architect"]?.map((rung) => rung.model), ["claude-fable-5-5", "claude-fable-5-1"]);
  });

  test("reads omo 5.1.69, whose category chains are bare arrays, with Fable 5.2 first", () => {
    const chains = extractChains(fixture("5.1.69"));
    assert.deepEqual(Object.keys(chains.categories), CATEGORIES);
    assert.deepEqual(Object.keys(chains.agents), AGENTS);
    for (const chain of [chains.categories["architect"], chains.categories["artistry"], chains.agents["plan-consultant"]]) {
      assert.deepEqual(chain?.slice(0, 2).map((rung) => rung.model), ["claude-fable-5-2", "claude-fable-5-1"]);
    }
  });

  test("fails with the format-changed message on an unknown category shape", () => {
    const unknown = fixture("5.1.69").replace('{"visual-engineering":[', '{"visual-engineering":new Set([');
    assert.throws(
      () => extractChains(unknown),
      (error) =>
        error instanceof OcxOmoError &&
        error.message === "category model chains not found in omo-task.js; its format changed",
    );
  });

  test("fails loudly when the chains are gone", () => {
    assert.throws(() => extractChains("var nothing=1;"), OcxOmoError);
  });

  test("fails loudly when a constant cannot be resolved", () => {
    const broken = MINIFIED.replace('qa={claudeOpus:"claude-opus-5-5",gptAstra:"gpt-6-astra"}', "qa=make()");
    assert.throws(() => extractChains(broken), /constant qa/);
  });
});

describe("escapeRegExp", () => {
  test("matches a name with RegExp metacharacters only literally", () => {
    for (const name of ["a$b", "a.b", "a\\b", "a+b*c?", "(a)[b]{c}|d^e", "a/b-c"]) {
      const pattern = new RegExp(`^${escapeRegExp(name)}$`);
      assert.ok(pattern.test(name), name);
    }
    assert.equal(new RegExp(`^${escapeRegExp("a.b")}$`).test("axb"), false);
    assert.equal(new RegExp(`^${escapeRegExp("a\\d")}$`).test("a1"), false);
  });
});
