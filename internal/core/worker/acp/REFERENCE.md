# ACP lifecycle reference observations

Read before implementation for issue #747. Design authority is
[the ACP adapter contract](../../../../docs/design/work-and-delegation.md#acp-adapter)
and C-29/C-60 in [Go Core](../../../../docs/design/go-core.md#work-engine).

- **Seen:** The shared client from #746 negotiates version 1, owns stdio children,
  separates replay updates and returns typed prompt stop reasons. CRW commit
  `821d6453aacfc9053b17c3b24a134c0ce4077f80`,
  `internal/bridge/appserver/bus.go`, correlates notifications by thread and
  explicitly signals lost observations. LINA's existing adapter boundary also
  binds the turn, process generation and binding epoch.
- **Inferred:** Each ACP prompt needs a Core turn identity. Streamed progress must
  precede the finish event; a missing prompt reply is a connection failure.
  Cancellation completes only on the prompt's `cancelled` reply. The existing
  dispatch verifier, rather than worker prose or process exit, derives receipts
  from observed Git and pull-request evidence.
- **Unknown:** Synthetic lifecycle tests do not verify installed vendor versions,
  permission routing, work-tool MCP, recovery, steering or vendor quota sources.
  Those integrations have separate issues. No Hermes or Honcho code is copied.

## Cursor readiness (#210)

- **Seen:** [The protocol measurements](../../../../docs/research/worker-protocols.md)
  record Cursor CLI 2026.10.22-27dcf5f answering ACP v1 without an agent version,
  and rejecting `session/new` with -32000 without sign-in. The built-in profile
  already runs `cursor-agent acp` and probes `cursor-agent --version`.
- **Inferred:** That explicit authentication refusal is a failed readiness
  check before any prompt. Close the partial client before returning a typed
  sign-in failure; do not expose the agent's error message or data. Other
  handshake failures do not prove that another worker can safely take over.
- **Unknown:** A live signed-in Cursor round trip requires the person's account
  and remains opt-in. Synthetic tests do not verify a vendor release. This
  change implements the existing ACP contract, not a CRW or Hermes port.

## Approval and form routing (#748)

- **Seen:** The pinned ACP v1 types distinguish `allow_once`, `reject_once`,
  `allow_always` and `reject_always`. `kit/acp` retains request handles, consumes
  each answer once, expires them on prompt completion, and sends pending
  cancellation replies before `session/cancel`. Core's existing worker bridge
  authenticates owner answers, claims approvals before delivery and stops unused
  answers across epochs. Its form values support strings, numbers and booleans.
- **Inferred:** Bind each handle to its current Core thread and expose an opaque,
  monotonically increasing request ID. Only select a unique offered once option;
  reject permanent options even if supplied directly to `Answer`. A form's
  declared schema must validate its answer before the handle is consumed.
- **Unknown:** Synthetic tests do not establish vendor support for elicitation,
  or verify a vendor release. This implements LINA's native ACP contract; no
  Hermes, Honcho, LCM-X or CRW behavior is copied.

## Session work tools (#749)

- **Seen:** LINA's existing Core work-tool handlers validate `work.Receipt` and
  bind claim event identities to the thread, process generation and binding
  epoch. The receipt carries its originating turn. The shared ACP client accepts
  stdio MCP declarations in `session/new`.
- **Inferred:** Reusing those handlers through `worker.ToolCallEvent` and `Answer`
  keeps Codex and ACP receipt and question semantics together. A per-session
  local socket must reject other sessions' tokens and calls outside an active
  turn; its process and socket must end with the session.
- **Unknown:** This integration does not establish behavior for any installed
  vendor version. It reuses the existing LINA contract; no external Hermes,
  Honcho, LCM-X or CRW behavior is newly ported.

The shared declarations live in `worker.WorkToolDeclarations`. `lina worker-tools`
implements MCP initialize, ping, tools/list and tools/call over bounded JSON lines.
`internal/clientapi` defines the private session socket request and response; it
never retries a possibly effective call. Core authenticates the token before
listing or calling, assigns the full execution identity itself, bounds concurrent
calls, and expires unanswered calls on completion. The stdio bridge receives the
socket and token through its per-session environment, not command arguments.
The socket and its private temporary directory are removed on close and failed
session startup. Persistent receipts remain unverified claims in the existing
conversation ledger, separate from dispatch's independently derived receipt.

The installed-Core work-tools scenario also answers `ask` through the authenticated
waiting-card client. Question forms carry the actual worker name; the reply returns
to the same pending MCP call before the worker submits its completion claim.

## Implementation notes

Design authority: [Go Core](../../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-60 worker profiles

`internal/core/worker/acp` implements C-29 on that client.
A profile holds the worker name, the command and arguments, the environment variables it sets (such as `CLAUDE_CODE_EXECUTABLE`), the version probe and the vendor rate-limit key it may read.
Claude Code, Cursor, Gemini CLI and Grok are built-in profiles; person-added profiles come from an editable settings file defined by their own issue.

## Once-only recovery (#751)

- **Seen:** The native shared client provides `ResumeSession` and `LoadSession`,
  marking replay at the read-loop boundary. Dispatch commits `Recoveries` and
  `RecoveryThread` before scheduling another attempt; its recovery policy turns
  a second settled failure into a persistent decision. Headless adapters retain
  uncertainty after tool activity.
- **Inferred:** A settled failed attempt needs a continuation entry point that
  starts the same command and worktree, restores the recorded session, then
  prompts it under a new process binding. This is different from reconciling an
  unknown turn after Core itself crashes. Loading a session cannot prove such
  an unknown turn or its effects settled.
- **Unknown:** Synthetic agents do not verify vendor releases or external
  effects. No Hermes, Honcho, LCM-X or CRW behavior is newly ported here.

`RecoverAttempt` is used only by dispatch after a committed recovery allowance.
It prefers advertised resume, otherwise load, otherwise a new session, with a
fresh session tool server. A restore error never falls back to another session.
Recovery uses `LoadSessionWithoutReplay` to exclude history before queue admission;
the ordinary shared-client load API still publishes tagged replay. Suppression
ends when the read loop consumes the load response, preserving live updates.
Terminal snapshots retain a copy of the finish and conservatively keep effects
unknown after tool activity, unknown update types or unfinished requests. Core
MCP admission latches tool activity even when an answered call is no longer pending.
