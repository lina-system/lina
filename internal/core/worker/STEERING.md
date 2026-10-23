# Steering delivery

## Reference observations

Read before implementation: reason-machines/reason-network at `72ca2b3`
(2026-10-22, MIT OR Apache-2.0), `DESIGN.md` (Getting messages into agents),
`src/daemon/mail.rs`, `src/mcp.rs` and `src/hook.rs`.

- **Seen:** the reference separates push (including idle wake), turn (tool/stop
  hooks) and pull (unread notices and `check_inbox`). Mail addresses use
  `<worker>:<session>`. Unread mail survives a temporary disconnection; expiry
  informs the sender. Its delivery is at least once, with explicit read tracking.
- **Inferred:** Core's existing authenticated worker bridge can implement the
  same delivery boundaries without installing hooks in the person's settings.
  Durable claims are necessary because replaying an uncertain push can repeat work.
- **Unknown:** vendor account-backed timing and omp/Antigravity native steering.
  Synthetic protocol tests do not establish installed-version compatibility.

## Connection mechanisms and evidence

The reference's vendor tier is not a claim that every LINA connection has that
transport. `worker.DeliveryProfile` describes the actual connection; the ACP
profile carries it into Core. Snapshot results and steering records expose it.

| Worker | Reference mechanism | LINA connection tier and mechanism | How checked |
| --- | --- | --- | --- |
| Codex | Push via app-server or queue, with hook fallback | Push: `turn/steer` with expected turn, `turn/start` when idle | Existing Codex protocol steering/admission tests; fake-worker mailbox test |
| Claude Code | Push through its own inbox socket; hook/channel fallbacks | `claude-acp`: turn at next LINA tool result, `session/prompt` when idle; headless `claude`: unavailable pull inbox, held then refused on terminal completion | ACP profile and bridge tests; headless adapter capability inspection. Native inbox socket not installed or claimed |
| Cursor | Turn: `postToolUse.additional_context`, `stop.followup_message` | Turn at next LINA tool result; `session/prompt` when idle | Built-in profile test and shared ACP bridge; upstream hook source read |
| Gemini CLI | Turn: `AfterTool.additionalContext`, `AfterAgent` block | Turn at next LINA tool result; `session/prompt` when idle | Headless running-Core test sends owner steering, worker writes and commits changed result, acknowledges IDs |
| Grok | Turn: `PostToolUse.additionalContext`, `Stop` block | Turn at next LINA tool result; `session/prompt` when idle | Built-in profile test and shared ACP bridge; upstream hook source read |
| OpenCode | Push via session prompt plugin | Added ACP connection: pull through unread notice and `check_inbox`; `session/prompt` when idle | Generic ACP fallback and fake pull test. Native plugin push not enabled |
| omp | No verified reference entry (pi's extension uses `sendUserMessage`) | Added ACP connection: pull through unread notice and `check_inbox`; `session/prompt` when idle | Generic ACP fallback and fake pull test. pi compatibility is unknown |
| Antigravity | No verified upstream mechanism | Headless connection: unavailable pull inbox; held then refused on terminal completion | Adapter capability inspection; gone-session mailbox test. No fabricated mid-turn transport |
| Copilot / other MCP clients | Turn hooks for Copilot; generic clients pull | Added ACP connection defaults to pull | Generic ACP profile and fake pull test |

A native tool call outside LINA does not contact Core. Turn connections receive
mail at their next **LINA** tool result. No MCP notification is treated as model
input. LINA installs no vendor hook or plugin to claim a stronger tier.

## Runtime evidence

`work_steering` is the mailbox projection. `work_steering_receipts` is the
append-only session receipt ledger; assigned tasks also record each change in
`work_ledger`, and their cards rebuild the latest receipts. Core's owner control,
manager `worker_input`, worker tools and recovery paths share these records.

`stored`, `delivered`, `read`, `held` and `refused` remain distinct. A message
claimed before transport is held with `delivery_uncertain`; restart does not
resend it. Known-unsent mail survives under its session key. Worker IDs in
`check_inbox.read` acknowledge reading; `acted` records a worker claim with
Core's observed send-to-action duration, in milliseconds, attributed to the
message's tier. This is not evidence of task acceptance. The Core e2e separately
checks the committed artifact. No timing assertion depends on a sleep winning.
