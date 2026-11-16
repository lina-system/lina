# feat(prompt): put the owner's standing rules in a frozen core section ahead of the self note, and changes in the turn note

Milestone: M3 Memory with boundaries
Labels: enhancement, size/M, omo-opus

## Summary

Every turn of an agent carries the rules that apply to it: all-agents rules, its own, and its place's. They sit in
one core section in the frozen plugin-section slot, ahead of the self note (`docs/design/standing-rules.md`
section 5). A rule made mid-session reaches running sessions as a one-shot note on the next user message, so the
system prompt is never rebuilt for a rule.

## Done when

- [ ] A core section `owner-standing-rules` renders in `_frozen_plugin_prompt_sections` before the self-note
      section: the boundary line, then all-agents, agent and place rules, one line each with its date; filter rules
      only as a count. An agent with no rules gets no section.
- [ ] The section restores from the stored prompt on resume and keeps its previous bytes when a render fails at a
      rebuild, like the self note.
- [ ] A rule change while a session runs adds one note to that session's next user message with the full
      difference against the frozen section; the system prompt bytes do not change until the next session start or
      compaction (test: 20-turn fixture, a rule added at turn 5, the system prompt identical on every request, the
      note on turn 6 only).
- [ ] The prefix tests named in `docs/design/agent-tastes.md` section 3 still pass.
- [ ] The cache hit rate on a 20-turn fixture is recorded before and after with `lina memory cache-report`; the change
      does not lower it.
- [ ] A rule set larger than the section cap (4,000 characters) is never cut silently: the chosen behaviour (decided
      here from real counts) is tested.
- [ ] The decision on workers (safe mode renders no plugin sections) is written down, and tested if they get the
      section.
- [ ] `docs/memory-layer.md` "Context order" takes rows 4 and 9 of `standing-rules.md` section 5.

## Context

- Part of lina#323. Depends on draft 01. Sits beside lina#359 (self note), lina#307 (tastes) and lina#380 (writing
  rule) in the same slot; decides whether the lina#380 writing rule moves into this section.
- Size: M
- Runner: omo-opus — context order and cache
