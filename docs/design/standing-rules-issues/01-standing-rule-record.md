# feat(memory): store the owner's standing rules as dated versions, written only from the owner's words

Milestone: M3 Memory with boundaries
Labels: enhancement, size/M, omo-opus

## Summary

The owner's standing rules (`docs/design/standing-rules.md`, lina#323) need one record in the store: one row per
version, scoped to all agents, one agent or one place, with the uid of the owner's sentence it came from. Only the
owner's own words can make a row, and the store checks that, not the prompt.

## Done when

- [ ] A migration (the next free number) adds `memory.standing_rules` with the fields of `standing-rules.md`
      section 4 (The record), idempotent (`IF NOT EXISTS`), applied by `lina store upgrade`.
- [ ] Row-level security: an agent reads its own rows and the shared `*` rows through `lina_app`; it reads no other
      agent's rows. `lina_app` has no direct INSERT, UPDATE or DELETE on the table.
- [ ] One write function records a new version from a source uid. It refuses a uid whose `memory.messages` row is
      not `speaker = 'owner'` or not in a session of the calling agent, and it marks the previous active version
      `replaced` in the same transaction. A test with a synthetic owner row, an agent row, a gateway note and an
      outside row: only the owner row writes.
- [ ] At most one active version per rule; a version's text, scope and source cannot be updated after it is written
      (a test tries and fails).
- [ ] Ending a rule writes an `ended` version; forgetting it goes through the forget path and leaves a tombstone.
- [ ] `grade` is `owner` on every row (lina#440); a row whose source is an `external` item cannot be written.
- [ ] `docs/store.md` gains a "Standing rules" section with the roles and policies.

## Context

- Part of lina#323. Depends on lina#440 (grades) for the grade column's meaning.
- The word list (`0016_word_list.sql`) is the closest precedent for write grants; the shared owner `*` comes from
  `0009_shared_external_vectors.sql`.
- Size: M
- Runner: omo-opus — store schema, row-level security and a write function that enforces authorship
