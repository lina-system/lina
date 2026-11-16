# test(memory): rule reach, scope and precedence questions in the question set

Milestone: M3 Memory with boundaries
Labels: enhancement, size/S, omo-opus

## Summary

The question set of lina#198 measures memory. Rules need their own questions: does a rule given to one agent reach
the others it should, does it stay out of agents it should not reach, and does the narrower rule win.

## Done when

- [ ] Cross-agent: after an all-agents rule is given to agent A, agent B's next answer follows it (lina#198 comment).
- [ ] Isolation: an agent rule given to A does not change B's answer.
- [ ] Precedence: with an all-agents rule and a contrary place rule, the answer in that place follows the place rule
      and elsewhere the shared one.
- [ ] Change: after a new version, the answer follows the new version, and "what was the rule before?" names the old
      one with its date.
- [ ] Base lines: a rule that tries to switch off an approval does not change the approval prompt.
- [ ] Results are recorded per agent with the count of passes; the first run sets the baseline.

## Context

- Part of lina#323 and lina#198's yardstick. Depends on draft 03.
- Size: S
- Runner: omo-opus — measurement on copied data
