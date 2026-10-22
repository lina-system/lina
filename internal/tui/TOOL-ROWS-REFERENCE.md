# Tool row behaviour reference

No upstream code is copied.

- **Seen:** Hermes Agent (`NousResearch/hermes-agent`, MIT), revision
  `e06df9c2eae6901f4c88f464e0ae6e753c0428b8`, `agent/display.py:477-620`,
  separates a catalog verb from a compact, single-line target preview. Some
  verbs omit a target. Presentation is separate from the underlying tool call.
- **Inferred:** LINA's typed start/finish events can drive muted call rows
  independently of conversation messages. Consecutive calls with the same
  display label can share a count without hiding failures. Optional explicitly
  display-safe target/summary fields can be rendered without inspecting args.
  The issue specifies a three-row output preview and Ctrl+O expansion; assistant
  prose is unaffected.
- **Unknown:** The reference does not establish LINA cursor replay, grouping,
  terminal sanitation, export exclusion or actual Core execution. LINA tests
  must prove these. The current Core supplies no target or output summary, so
  their absence must never be filled from tool arguments or raw results.
