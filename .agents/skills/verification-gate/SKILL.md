---
name: verification-gate
description: "Require fresh evidence before declaring work complete."
version: 1.0.0
author: LINA (adapted from oh-my-hermes)
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [verification, evidence, quality]
    category: software-development
    related_skills: [code-review, work-ledger]
---

# Verification Gate Skill

Turn a completion claim into observed checks and a PASS, HOLD, or BLOCK verdict. Keep implementation, review, CI, and merge evidence separate.

## When to Use

Before claiming a change is fixed, verified, passing, or ready to merge.

## Prerequisites

Obtain the claim, expected behavior, exact tree revision, repository check requirements, and available local or CI evidence.

## How to Run

Use `read_file` and `search_files` for requirements and declared generated outputs; use `terminal` for the project's own verification commands. Do not modify requirements to make coverage appear complete.

## Quick Reference

| Verdict | Condition |
|---|---|
| PASS | Every required check passed with fresh evidence covering the change. |
| HOLD | Evidence is missing, stale, unavailable, or a required failure is pre-existing or of unknown origin. |
| BLOCK | A required failure was introduced, or unsupported completion claims conceal unfinished work. |

## Procedure

1. Check repository declarations for generated paths before editing them. Record source, output, regeneration command, declaration location, and gate (or "no gate"). If absent, report "map not declared"; never infer generation from filenames alone.
2. List required and optional build, lint, type, test, security, documentation, generated-output, and CI checks. Tie each claim to a check that observes it; broaden for shared contracts, authentication, secrets, migrations, or financial effects.
3. For written requirements, reuse stable IDs or assign IDs in document order and disclose that assignment. Record requirement -> task -> evidence, marking inferred links. Identify ambiguity, conflicts, missing work, and missing evidence; a filled table is not proof.
4. Run checks or read actual CI results. Per result record the exact command, source, exit status, output summary, covered scope, revision, freshness, and failure origin. Label owner-supplied results as reported rather than personally observed.
5. Classify failures as `introduced`, `pre_existing`, or `unknown`. Prove `pre_existing` by running the same command with the same failure at the merge-base in a separate clean checkout. An observed base run outranks a changed-line inference; without proof use `unknown`. Never disturb the user's working tree to obtain a baseline.
6. Require every named CI gate to complete successfully on the exact head. Missing, skipped, pending, or cancelled jobs are not green. For branch health inspect the newest completed, non-cancelled run; do not substitute a list with no failures for a successful run.
7. When failures appear intermittent, compare failing cases across runs and reproduce on the affected platform before assigning blame. Fix the cause; do not hide nondeterminism with retries, skips, or longer timeouts.
8. Return the verdict, check records, requirement gaps, generated-output provenance where relevant, and the exact next check or fix for each HOLD/BLOCK.

## Pitfalls

- Never PASS beside any failing required check, including a proven pre-existing failure. A collection failure that prevented coverage is missing evidence, not a completed check.
- Never treat plans, placeholders, old output, another tree's green run, or local success replacing required CI as proof.
- Refuse completion for unlinked TODO/FIXME/stubs, suppressed tests without a linked reason, circular evidence, or proof words without observed checks; name the excerpt and remedy.
- When removing a trust-boundary guard or its negative test, require a named regression or adversarial case proving refusal still works. A guard moved intact is not a removal.
- Do not install dependencies, trigger external jobs, push, or merge without the required owner authorization.

## Verification

Every required check has a fresh result or an explicit "not run" gap. Each failure names its origin and proof; each verdict states what remains and how to resolve it. Use `code-review` for defect judgment and `work-ledger` to attach verified evidence to the work record.

Adapted from oh-my-hermes (https://github.com/rlaope/oh-my-hermes, commit 59270ab48e82ab24cfe4f445dc5693b97d4141a3). MIT License, Copyright (c) 2026 oh-my-hermes contributors; the license text is the MIT text in LICENSES/Hermes-MIT.txt with that copyright line.
