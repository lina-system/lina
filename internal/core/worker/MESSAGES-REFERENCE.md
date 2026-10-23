# Worker message reference observations

Reference read before implementation for #980: `reason-machines/reason-network`
at `72ca2b3fb36d6e3cbfa44aa82c68d27aafe5e690` (MIT OR Apache-2.0),
`src/render.rs`, `src/daemon/mail.rs`, `src/files.rs`, `src/mcp.rs`,
`src/team.rs`, `src/hook.rs`, `src/doing.rs` and `DESIGN.md`.

- **Seen:** message frames distinguish peer input from person input. The
  renderer escapes attributes and defangs frame-like body delimiters. Mail
  limits automatic pair exchanges to twelve in ten minutes. MCP addresses one
  session. Signed approvals quote requests and bind the approver and recipient.
  Text attachments are confined to visible project paths, with ten-file,
  256-KiB individual and 512-KiB combined limits and non-executable saved copies.
- **Inferred:** LINA uses a durable per-task allowance and trusted decision
  reset, because workers coordinate only with Core. Core's authenticated
  approval UI supplies the decision; adapters pin Core's verification key.
  XML escaping all text avoids porting the Rust delimiter matcher.
- **Unknown:** the reference does not establish support for signed metadata
  in each vendor protocol. ACP has an explicit metadata field; Codex receives
  its native permission response after adapter signature verification. No live
  vendor compatibility claim follows from synthetic protocol fixtures.

Design authority:
[Work and delegation](../../../docs/design/work-and-delegation.md#lina-tools-for-workers)
and [Approvals](../../../docs/design/work-and-delegation.md#approvals).

Core frames task starts, follow-ups, recovery, tool results and free-text
question answers at its worker boundary. The message admission tables retain
identities and counts without granting execution authority. Approval signatures
are retained with canon's authenticated decision and consumed before delivery;
private signing keys never enter worker setup or canon.

The steering mailbox added in #979 keeps sender and speaker kind with each
original text record. Delivery frames that source for push, turn and pull
connections; tool-result envelopes are framed separately. Old records with no
source metadata remain system evidence. Source is part of idempotent message
identity, and owner decisions reset the per-task allowance only once.
