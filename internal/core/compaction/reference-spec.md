# Prepared summaries: reference behavior

Reference: LCM-X v0.28.0, commit
`709099b3a397a7893a7abae8802ebe7bf605f79e`. Paths below are relative to
[that revision](https://github.com/electricsheephq/lcm-x/tree/709099b3a397a7893a7abae8802ebe7bf605f79e).
This specification was written before the Go implementation.

- **Seen** — `leaf_prep.py:3-13,73-85`: capture a turn-end snapshot; preparation
  does not publish nodes or change the foreground request. Only completed
  preparations can be promoted. `tests/test_787_slice1_background_leaf_prep.py:219-251`
  verifies promotion without another foreground model call.
- **Seen** — `leaf_prep.py:117-162`: bind a preparation to summary configuration
  and an ordered source digest. The tests at
  `tests/test_787_slice1_background_leaf_prep.py:341-395` reject edited source
  content, a model change and a moved publication frontier.
- **Seen** — `leaf_prep.py:351-368,452-464,699-701`: preparations must start at
  the live frontier; competing foreground publication invalidates late work.
- **Seen** — `dag.py:148,267-321`: nodes carry depth and source provenance;
  node publication and related writes commit or roll back together.
  `tests/test_dag_source_message_ids.py:51-67` follows nested nodes to ordered
  raw sources. `tests/test_issue_977_routine_condensation_depth.py:65-77`
  distinguishes shallow and deeper condensation groups.
- **Inferred / LINA mapping** — Core's immutable ledger, exact coverage partition,
  root revision and revocation epoch replace mutable host-list reconciliation.
  The worker requests the conversation model through Aux and accepts a result
  through Apply; an opaque committed job ticket authorizes a volatile preparation, not a node.
  Summary text never enters durable job receipts; restart discards the cache.
  A foreground read never waits for a running job. Appended verbatim events may
  refresh the fence only when summarized sources, root revision, epoch and
  summary configuration still match. A failed or stale preparation falls back
  to on-demand summarization.
- **Inferred / LINA mapping** — enqueue only after delivery when the complete
  request reaches the configured compaction threshold. Use the same segment
  selection as the foreground pass. There is no autonomous rollup producer.
  Successive demanded compactions form levels 0, 1, 2 and beyond. Retraction
  rebuilds invalidated nodes bottom-up with their ancestors and commits together.
- **Unknown upstream / specified by LINA** — no LINA revocation epoch or Core
  ownership fence exists in these reference files. Their semantics come from
  C-18 and the compaction-and-resume contract, not from an assumed upstream test.

No upstream implementation or prompt is copied. See
[third-party notices](../../../THIRD-PARTY-NOTICES.md#lcm-x).

## Successive summaries: carried values

Reference: the same pinned revision, inspected before this change. The local
Hermes installation was unavailable; these files were read from that revision.

- **Seen** — `dag.py:267-325` stores each summary and its source ids atomically.
  `compaction.py:_condense_before_leaves,_condense_after_leaves` separates leaf
  generation from condensation of an oversized summary prefix. Earlier leaf
  summaries are not rewritten for every new leaf.
- **Inferred / LINA choice** — preserve the existing single-parent root and its
  coverage transaction. Carry its specific values verbatim when adding events,
  while the model updates the other sections and extracts new values. Bound the
  carried section; condense it only when it exceeds that bound or an actual
  root-only fitting pass requests compression. This avoids repeatedly rewriting
  old exact values without introducing a second publication topology.
- **Unknown** — this is not LCM-X's leaf condensation algorithm. Its quality and
  request-size tradeoff must be measured on the fixed #849 replay. Neither the
  reference nor synthetic tests prove semantic retention on that history.

## Queued inputs at a turn boundary

Reference: the pinned LCM-X revision above, inspected before this fix.

- **Seen** — `dag.py:267-325` publishes a summary and its provenance together
  and permits related writes in that transaction.
- **Inferred / LINA mapping** — a later queued person or forwarded input is
  part of the publication frontier, but not the current request or summary.
  Account for its id in the retained ledger tail, leaving its body untouched
  for its own turn. Keep the complete frontier and ordered-prefix checks.
- **Unknown upstream / specified by LINA** — this reference does not define
  Core's queued-input identity or model-context filtering. Those boundaries
  come from LINA's preparation and request-layout contracts.

## Coverage order and restored exclusions

Reference: the same pinned LCM-X revision above, inspected before this fix.

- **Seen** — `dag.py:267-325` publishes a node and its provenance atomically;
  `dag.py:884-925` resolves nested provenance to raw messages in store-id order.
- **Inferred / LINA mapping** — publication must preserve ledger append order:
  a summary covers a prefix before the retained verbatim tail. Set membership
  alone cannot prove that ordering. Event timestamps do not define ledger order.
  Excluded events in the tail are checked separately and stay outside the node
  until a later prefix reaches them; they must not extend its coverage past a
  retained message.
- **Inferred / LINA compatibility** — previously stored trees can own exclusions
  past a retained message. Validate those inherited reasons and account for the
  exclusions at their ledger positions, preserving stored provenance. They do
  not establish a summary input boundary; actual inputs and new coverage retain
  the strict ordering checks.
- **Unknown upstream / specified by LINA** — these reference paths do not
  establish LINA's revocation/restore semantics or skipped-event reasons. On
  restore, reconstruct exclusions from canonical replay/scaffold metadata so
  restoring visibility cannot turn a replay, scaffold or summary echo into
  summary input. Native provider items remain eligible as conversation content.

## Cache-aware timing and a summary that reuses the cached prefix

Reference: the pinned LCM-X revision above, plus the provider measurements in
[provider-optimization.md](../../../docs/research/provider-optimization.md#compaction-summary).
These files were inspected before this change; no source or prompt is copied.

- **Seen** — `engine.py:2263-2275`: a boundary skip arms a 60-second
  no-compress window, so the reference times its own compaction around recent
  boundary decisions rather than only around token counts.
- **Seen** — `compaction.py:1228-1240,1327-1340`: a leaf summary input is the
  selected messages themselves, with the previous node's text carried
  separately; the summariser never receives the retained tail.
- **Inferred / LINA mapping** — LINA's timing is provider-cache timing, not a
  wall-clock cooldown: a boundary whose conversation cache has expired may
  compact at P (70%) because the rewrite costs no cache, and a warm cache keeps
  the 90% trigger. The age comes from the recorded request time and the
  lifetime the route's capability set admits; an unadmitted or undeclared
  lifetime leaves the expiry unknown, which never counts as expired.
- **Inferred / LINA mapping** — the summary request is the conversation's own
  request truncated at its segment: fixed instructions, tool declarations,
  cache identity and the marks inside the prefix are carried unchanged, the
  segment's items follow, and the summary instruction comes last. Tools and
  tool choice are untouched and Core runs no tool call from that response.
- **Unknown upstream / specified by LINA** — LCM-X has no provider cache
  lifetime, cache identity or cache-mark concept, and no tool-call-free summary
  guard. Those rules come from LINA's capability set and request layout, and the
  measured gain is the provider replay's summary row, not an upstream test.
