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
- **Unknown upstream / specified by LINA** — these reference paths do not
  establish LINA's revocation/restore semantics or skipped-event reasons. On
  restore, reconstruct exclusions from canonical replay/scaffold metadata so
  restoring visibility cannot turn a replay, scaffold or summary echo into
  summary input. Native provider items remain eligible as conversation content.
