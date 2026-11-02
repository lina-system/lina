---
name: writing-review
description: "Edit prose while preserving facts and voice."
version: 1.0.0
author: LINA
license: MIT
platforms: [linux, macos]
metadata:
  hermes:
    tags: [writing, editing, factuality]
    category: productivity
    related_skills: [humanizer]
---

# Writing Review Skill

Revise documents, reports, messages, and interface copy without inventing facts.
Unlike `humanizer`, this skill governs the editing and factual review workflow rather than cataloguing AI writing patterns.

## When to Use

Use for a prose edit, rewrite, or review; skip code, commands, and short chat replies.

## Prerequisites

Read the original with `read_file`; retain its audience, length, tone, and format constraints.
The optional checker requires Bash and standard POSIX utilities.

## How to Run

Use `terminal` to run `bash scripts/words-check.sh <draft-file>` relative to this skill.
The list in `references/banned-words.txt` contains English style warnings, not mandatory substitutions.
No Korean list is retained; the source list described local interface labels rather than general writing style.

## Quick Reference

- Edit: return the revised prose and unresolved factual questions.
- Review only: quote the problem, explain it, and suggest a replacement, most important first.
- Checker exits: 0 clean, 1 warnings, 2 invocation or read failure.

## Procedure

1. Determine whether the request is an edit, rewrite, or comments only from context.
2. Extract a fact checklist: numbers, decisions, conditions, names, and examples that must survive.
3. Cut empty modifiers, repeated ideas, overloaded sentences, and explanatory dashes.
   Replace change-history narration with the current fact unless history is the subject.
4. Remove incidental tool jargon only where it is not needed to reproduce a procedure.
   Order sections by one clear criterion; preserve headings and required structure.
5. Compare the draft against the fact checklist. Restore omissions and remove invented claims.
   Preserve code blocks, inline code, URLs, and legal meaning exactly.
6. Run the checker; inspect each warning in context. Verify dates and factual claims against evidence;
   mark unresolved claims rather than silently correcting them.
7. For consequential prose, ask an independent reader what is unclear or requires guessing.
   Fix demonstrated problems, then return the requested artifact with a short verification note.

## Pitfalls

- Do not add sections, motivation, anecdotes, or conclusions that were not requested.
- Do not flatten the author's voice, replace precise labels with invented metaphors, or change quoted text.
- Do not send credentials to a reviewer or publish the result without authorization.
- Do not treat a word-list match as proof that a sentence is wrong.

## Verification

Every required fact remains, no unsupported fact appears, and all protected formatting survives.
Report unresolved claims and whether a separate reader reviewed the draft.
