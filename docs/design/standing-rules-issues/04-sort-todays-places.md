# feat(memory): move today's rules out of SOUL, user notes, Honcho conclusions, skills and job prompts with the owner's yes

Milestone: M3 Memory with boundaries
Labels: enhancement, size/M, omo-opus

## Summary

Today the owner's rules sit in shared rule files, each agent's `SOUL.md`, `USER.md`, `MEMORY.md`, Honcho
conclusions, skills and scheduled-job prompts (`docs/design/standing-rules.md` section 7). This collects the
candidate lines, asks the owner once in conversation which are rules, writes those as rule rows, removes them from
where they were, and stops the writers that keep putting rules there.

## Done when

- [ ] A read-only census per agent lists candidate rule lines in each place (counts only in logs and reports, no
      owner text).
- [ ] The owner gets the candidates as one short list in conversation and answers by talking; only confirmed lines
      become rule rows, with the confirming sentence as source.
- [ ] Confirmed lines are removed from their original place, with an undo snapshot taken first (`lina undo snapshot`).
- [ ] The background review no longer routes "expectations about how you should behave" into `USER.md`
      (`agent/background_review.py`); it offers them as rules instead.
- [ ] The `honcho_conclude` description no longer invites standing constraints; it points to the rule path.
- [ ] Filter-type lines (skills, job prompts, room lists) become filter rules that lina#300 applies.
- [ ] Shared rule files on the install are listed first; how the install puts them in front of agents is written
      down.

## Context

- Part of lina#323. Depends on draft 03.
- Touches prompts and real profile files: run on a copied home first (`docs/test-copy.md`).
- Size: M
- Runner: omo-opus — real data, owner-confirmed moves, undo
