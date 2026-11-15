# feat(agents): make, change and end a standing rule from one sentence of the owner

Milestone: M3 Memory with boundaries
Labels: enhancement, size/M, omo-opus

## Summary

The owner makes rules by talking: "never tell me to get approval", "everyone, no flourishes", "in this room only
what mentions me". The agent that hears it records the rule at once, reads the scope from the wording, asks once
when it cannot tell, and confirms in a few words inside its reply (`docs/design/standing-rules.md` sections 3 and 4).

## Done when

- [ ] A tool records, changes, ends and lists rules through draft 01's function; it takes the uid of the owner's
      sentence, never free text as the source.
- [ ] Scope from wording: on a fixed synthetic set of sentences in Korean and English, the recorded scope matches the
      label; an unclear sentence gets one one-line question and the narrowest scope until answered.
- [ ] A sentence that would cross a base line (approval, honesty, secrets, the owner's decisions) records nothing and
      gets a one-line reason.
- [ ] A fact correction ("that's wrong, it is X") goes to memory, not to rules; a sentence with both records both.
- [ ] Changing a rule writes a new version; "what rules do I have?" lists active rules, and with history shows
      replaced versions with their dates.
- [ ] When two or more agents hold the same agent rule, the owner is offered one shared rule once, inside an
      ordinary reply; a yes writes it and replaces the agent rules, no answer changes nothing.
- [ ] A rule offered from a memory pattern is written only after the owner's yes, with the yes as its source.
- [ ] Agent text the owner pastes back into a message is never the source of a rule (lina#324 speaker kinds).

## Context

- Part of lina#323. Depends on drafts 01 and 02. Absorbs the working-style list of the lina#198 comment and the
  record and one-sentence revert of lina#300.
- Size: M
- Runner: omo-opus — conversation judgment across agents, with a labelled sentence set
