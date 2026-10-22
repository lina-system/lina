# Activity behaviour reference

This is a behavioural port; no reference implementation is copied.

- **Seen:** Codex CLI (`openai/codex`, Apache-2.0, revision `86e17e98`),
  `tui/src/status_indicator_widget.rs:76–90,226–265`, places a glyph, working
  word, compact elapsed duration and interrupt hint above the composer. Its
  completed turn has a muted duration line. Hermes Agent
  (`NousResearch/hermes-agent`, MIT, revision `e06df9c2`),
  `ui-tui/src/components/appChrome.tsx:52–83,138–188`, uses frame tables,
  a one-second elapsed refresh, and pauses motion under overlays.
- **Inferred:** LINA can use its typed active flag, accepted output deltas and
  interrupt request to show thinking, replying and stopping. Animation adds
  emphasis; the word and duration must remain useful with a static glyph.
- **Unknown:** These references do not establish LINA's server clock offset,
  snapshot restoration or protocol ordering. LINA's reducer and installed-Core
  tests establish those separately. Tool phases cannot be inferred from text.

LINA uses one row above the composer, a 100 ms frame interval while visible,
elapsed seconds, and no idle timer. Reduced motion and monochrome output keep
the first frame. Completion durations are client display state, never history.

## Tool activity

- **Seen:** Hermes Agent (`NousResearch/hermes-agent`, MIT), revision
  `944c3a434071f43e64b443d033f3eba30c64434c`,
  `agent/display.py:531–620,1326–1349`, resolves friendly tool verbs from a
  catalog for the status phrase and appends duration to completion rows.
  The source was read through GitHub at that revision; no code is copied.
- **Inferred:** LINA can project a trusted catalog label at actual tool start,
  then a bounded success/failure indication after its durable result commits.
  Its existing turn journal supplies ordering independently of the renderer.
  The status needs no argument previews or output, so neither goes on this wire.
- **Unknown:** This reference does not establish LINA protocol compatibility,
  concurrent-call ordering, cancellation, model-history exclusion or recovery.
  LINA conformance, journal and installed-Core tests must establish those.
