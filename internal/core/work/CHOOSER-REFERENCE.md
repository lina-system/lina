# Worker choice reference

Read before implementation for #138. The authority is C-57 and the
[worker choice contract](../../../docs/design/work-and-delegation.md#choosing-the-worker).

- **Seen:** CRW pin `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
  `contract/schema/completion-receipt.json`, binds each receipt to an execution
  generation, attempt and terminal turn. Repeated execution-only outcomes have
  distinct identities. LINA's existing adapters expose typed quota failures and
  quota rows, including unknown windows and explicit reset times.
- **Inferred:** persist the chosen worker and attempt before launch. Keep the
  task packet and area reservation across a quota handoff, reap the old process
  first, and admit only one replacement. An uncertain attempt cannot be replayed
  after restart. Quota state and worker completion do not establish delivery.
- **Unknown:** the CRW receipt contract does not define a worker chooser or area
  scheduler; those follow LINA's contract. Hermes references are unavailable.
  No CRW executable, relay or review workflow is used. Synthetic vendor streams
  can prove Core routing, not live subscription limits or worker quality.
