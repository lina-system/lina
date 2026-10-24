# Worker protocols: one ACP client for coding workers

Research for [#737](https://github.com/thisisjun786/lina/issues/737). It compares the protocols LINA can use to drive the coding tools a person installed (workers), measures them on one host, and defines the evaluation that the implementation must pass. It is evidence, not a contract. The decisions it supports are in [work-and-delegation.md](../design/work-and-delegation.md#workers) and decision C-60 of [go-core.md](../design/go-core.md#work-engine).

## Result

- **ACP version 1 is the common worker connection.** Claude Code (through the `claude-agent-acp` adapter), Cursor (`cursor-agent acp`), Gemini CLI (`gemini --acp`) and Grok speak it, and so does any ACP agent the person installs. One Go client in Core serves all of them.
- **Codex stays on its app-server.** Both Codex ACP adapters measured worse than the app-server LINA already uses. One embeds its own Codex and could not read the person's configuration. The other runs `codex app-server` underneath and drops the rate-limit windows. There is no ACP fallback for Codex.
- **Antigravity stays on its headless stream.** Its ACP entry is a separate server package, not the `agy` the person installs.
- **The conversation providers stay separate.** The Claude provider must load none of the person's settings, and `claude-agent-acp` always loads them. The Gemini provider is an API key, not a command. Only the Grok provider shares the ACP client.
- **A2A is for later.** It connects remote agents over HTTP. It does not drive a local coding tool. It fits a later issue for LINA agents and Nodes talking to outside agents.
- **ACP has no quota signal.** Quota and typed failures come from JSON-RPC errors, stop reasons and vendor extensions where an agent offers them. Otherwise they are unknown, and LINA's own usage accounting applies.

## Method

- One Linux x86-64 host, 2026-10-23. Installed and signed in: Claude Code 2.1.309 and Codex 0.163.0, each with the person's own setup. Not installed: Cursor CLI, Gemini CLI, Grok Build and Antigravity. Cursor CLI 2026.10.22-27dcf5f, Gemini CLI 0.65.0 and Grok Build 1.0.68 were unpacked into temporary directories and run with an empty home, so only their handshakes could be measured.
- A throwaway ACP client, kept outside the repository, sent `initialize`, `session/new`, three prompts (a one-word reply, a file write that needs approval, and a long count that was cancelled), then killed the agent process, started it again and sent `session/load` and `session/list`. Permission requests were answered with reject. The probe ran in an empty Git directory. No repository content and no secrets were sent.
- The adapters and the tools not installed here came from npm or the vendor's own download, each into its own temporary directory with lifecycle scripts disabled: `@agentclientprotocol/claude-agent-acp` 0.94.0, `@agentclientprotocol/codex-acp` 2.2.2, the deprecated `@zed-industries/codex-acp` 0.16.0, `@google/gemini-cli` 0.65.0, `@xai-official/grok` 1.0.68 and the Cursor CLI package.
- The ACP method and update lists come from the JSON Schema shipped in `@agentclientprotocol/sdk` 2.0.0. Facts not measured here are taken from the sources at the end and marked as documented.
- Raw transcripts stay off the repository. Fixtures built from them must be synthetic ([go-roadmap.md](../design/go-roadmap.md#phase-rules), rule 7).

## Protocols

### ACP

The Agent Client Protocol (Apache-2.0) connects a client to a coding agent over JSON-RPC on standard input and output, one message per line. The client starts the agent as a child process.

- Version 1 is what every agent measured here negotiated. The latest schema release is 1.26.0 (2026-10-16). Its stable methods: `initialize`, `authenticate`, `logout`, `session/new`, `session/load`, `session/resume`, `session/list`, `session/close`, `session/delete`, `session/prompt`, `session/cancel`, `session/set_mode`, `session/set_config_option`, `$/cancel_request`, the agent-to-client requests `session/request_permission`, `fs/*`, `terminal/*` and `elicitation/*`, and the `session/update` notification. `session/fork` is still unstable.
- `session/update` variants in version 1: message, thought and user chunks, `tool_call`, `tool_call_update`, `plan`, `usage_update`, `session_info_update`, `available_commands_update`, `current_mode_update`, `config_option_update`, `compaction_*` and `notice`. Sub-agent updates (`subagent_update`) are unstable.
- A turn ends when the `session/prompt` request returns, with `stopReason` one of `end_turn`, `max_tokens`, `max_turn_requests`, `refusal` or `cancelled`. A cancel is a `session/cancel` notification. The client must answer pending permission requests with `cancelled`.
- Permission requests offer `allow_once`, `allow_always`, `reject_once` and `reject_always`.
- `session/new` takes the working directory and the MCP servers the agent should connect to for that session. Every agent must accept stdio MCP servers. This is a per-session way to hand LINA's work tools to a worker without installing anything in its setup.
- ACP defines no quota or rate-limit message. Authentication failures use JSON-RPC error -32000.
- The SDK schema already carries a version 2, still an alpha upstream. In it, `session/load` is gone and the `session/prompt` reply only confirms that the prompt was accepted; the turn ends with a `state_update` that turns idle with a stop reason. Every measured agent answered version 1. `claude-agent-acp` can already speak version 2 when told to.
- There is no official Go SDK. The community Go SDKs are Apache-2.0 and trail the schema; one still tracks schema 0.13. LINA generates its own types from the schema, as it does for Codex.

### A2A

A2A (Agent2Agent, Linux Foundation, Apache-2.0; 1.0.0 on 2026-03-12, 1.0.1 on 2026-05-28) connects independent agents over HTTP with JSON-RPC, gRPC or REST: Agent Cards describe an agent, tasks carry the work, and results stream or arrive by push notification. It assumes a network service on each side and has no notion of a working directory, a local permission prompt or a child process. Coding tools do not offer it. Its roadmap says as much: the project's answer is `a2a-cli`, a separate command plus a skill that lets a coding tool call A2A agents, not an A2A mode inside the tools. There is an official Go SDK (`a2a-go`, Apache-2.0). It fits LINA later, in two directions: a LINA agent published as an A2A agent, and a LINA agent or Node calling a remote A2A agent. Neither is a worker connection. The [A2A design research](a2a.md) pins the 1.0 wire details and informs the [integration contract](../design/integrations.md#a2a).

### Codex app-server

`codex app-server` is Codex's own JSON-RPC interface on stdio, the one its rich clients use. LINA's Codex adapter already drives it ([work-and-delegation.md](../design/work-and-delegation.md#codex-adapter)): threads with resume, `turn/steer`, typed approval requests, dynamic tools in a `lina` namespace, and `account/rateLimits/read` with update notifications. Upstream calls it experimental and not supported for production workloads; LINA absorbs that with generated types and a list of verified versions.

## What each worker offers

Seen means measured on this host on 2026-10-23. Documented means read in the source named at the end. Not seen means it could not be measured here.

| Worker and connection | Version | Session and resume after kill | Permissions | Streaming | Cancel | Quota signal | Sub-agent events |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Codex, `codex app-server` (LINA's adapter) | 0.163.0 | Thread resume (in the adapter) | Command, file and permission requests (in the adapter) | Turn and item events (in the adapter) | `turn/interrupt` (in the adapter) | Rate-limit windows from `account/rateLimits/*` (in the adapter) | Not used by LINA |
| Claude Code through `claude-agent-acp` | 0.94.0 on Claude Code 2.1.309 | Seen: `session/new`; after a kill, `session/load` replayed the history. `resume`, `list`, `fork`, `close` advertised | Seen: an edit request with once, always and reject; reject left no file | Seen: message chunks, `tool_call` and updates, `usage_update` | Seen: `cancelled` about 1 s after the cancel | Vendor extension: the Claude rate-limit event inside `usage_update._meta`; typed failures only through an opt-in extension | In the adapter's code: an opt-in sub-agent extension; not seen |
| Codex through `codex-acp` (new) | 2.2.2 on Codex 0.163.0 | Seen: new, load with replay, list; `resume`, `fork`, `subagents` advertised | Not seen: the person's own Codex reviewer approved the escalation itself | Seen: chunks, thoughts, tool calls, usage, thread status | Seen: `cancelled` | Lost: rate limits stay inside the adapter for its `/status` command | Advertised, not seen |
| Codex through `codex-acp` (Zed, deprecated) | 0.16.0 | Not seen: it embeds its own Codex, which exited on the person's configuration (`unknown variant max`) | Not seen | Not seen | Not seen | Not seen | Not seen |
| Cursor, `cursor-agent acp` (native) | 2026.10.22-27dcf5f | Seen in the handshake: `loadSession`, `list`. No version in `initialize` | Documented: once, always, reject | Not seen (no account here) | Not seen | None in the handshake | Documented: child sessions |
| Gemini CLI, `gemini --acp` (native) | 0.65.0 | Seen in the handshake: `loadSession`; no list or resume | Documented | Not seen (no account here) | Not seen | None in the handshake | None in the handshake |
| Grok Build, `grok agent stdio` (native) | 1.0.68 | Seen in the handshake: `loadSession`, `list`, `resume`, `close`. The version is only in `_meta` | Documented | Not seen (no account here) | Not seen | None in the handshake | Not seen |
| Antigravity, `agy` headless stream (LINA's adapter); the ACP registry's separate server not measured | Not installed | No resume in the adapter | Denied actions reported after the run, no live approval | `step_update` events | Process stop | None | None |
| Claude Code, `claude --print` stream (LINA's adapter) | 2.1.309 | No resume in the adapter | Denials reported after the run, no live approval | Assistant messages | Process stop | None in the adapter; the stream carries rate-limit events it does not read | None |

Without a sign-in, `session/new` failed with JSON-RPC error -32000 for Cursor and Grok ("Authentication required") and for Gemini CLI ("Gemini API key is missing or not configured"). That is how readiness can tell "needs sign-in" apart from a broken connection.

## Findings that shape the design

1. **An adapter can replace the person's worker version.** `claude-agent-acp` ships its own Claude Code (2.1.303 here, older than the installed 2.1.309) and uses it unless `CLAUDE_CODE_EXECUTABLE` names another one. With that variable set to the installed `claude`, a wrapper showed the adapter starting the person's 2.1.309. Installed without optional packages, the adapter has no Claude Code copy at all (107 packages and 69 MB instead of 342 MB) and still opened a session with the installed `claude`. The owner chose to have LINA pin and install this program, like the Claude adapter. `codex-acp` 2.2.2 likewise ships Codex 0.160.1 unless `CODEX_PATH` is set. The Zed `codex-acp` 0.16.0, archived upstream, compiles in Codex 0.137.0 and cannot use the installed one at all. The ACP registry's Antigravity entry is likewise a separate 336 MB server download, not the person's `agy`. So a worker profile always points the adapter at the installed tool, and the recorded version is the tool's, beside the adapter's.
2. **The adapter adds flags, not settings.** `claude-agent-acp` starts Claude Code with `--permission-prompt-tool=stdio`, `--setting-sources=user,project,local`, `--disallowedTools=AskUserQuestion` and the person's own default permission mode. It writes no file into the person's setup. Its edit-preview hook is a callback inside the adapter's own process. `codex-acp` adds a hook setting only when the client passes one. LINA passes none.
3. **The provider isolation contract rules ACP out for conversation.** The Claude provider must load none of the person's settings, skills or hooks ([runtime.md](../design/runtime.md#providers)). `claude-agent-acp` always passes all three setting sources. The Claude provider (#166, #168) therefore keeps its own adapter, and only the Grok provider (#170) shares the ACP client.
4. **Recovery works through the protocol.** After a kill, a new process loaded the same session by id and replayed its history, for both Claude Code and Codex through ACP. Cursor, Gemini CLI and Grok advertise `loadSession`, and Grok also `session/resume`. Once-only recovery in the same work area therefore uses `session/load`, or `session/resume` where it is advertised, with the session id Core recorded.
5. **Approval routing is the worker's choice, as the contract says.** Claude Code asked before a file write. Codex's own reviewer, set in the person's configuration, approved a network command without asking the client. LINA routes whatever the worker asks and imposes no mode. LINA never selects `allow_always` or `reject_always`, because they change the worker's remembered rules.
6. **Quota needs a per-worker source.** Through ACP, Codex loses its windows, and Claude Code carries them only in vendor metadata. Typed failures (rate limited, quota exhausted, sign-in needed) exist only in the opt-in JetBrains "AIR" session-failure extension that both adapters implement. The ACP adapter reads these sources where a worker offers them and reports unknown otherwise.
7. **Each tool adds its own extensions.** Grok's handshake also advertises its own hook and tool extensions, and carries host details in `_meta`. Cursor documents its plan, to-do and question extensions. The adapter reads only the standard fields and the extensions this note names, and records only the version and capabilities of a handshake.
8. **Steering is an extension.** Both adapters advertise `_meta.steering.supported`, and Claude Code advertises prompt queueing. Plain ACP has no steering. The adapter declares the steer capability only for a worker that advertises it.
9. **Version 2 changes the turn end.** The adapter pins protocol version 1 in `initialize` and refuses any other answer, like the Codex adapter's verified-version rule. Version 2 is a later change with its own verified mapping.

## Evaluation, defined before building

The same small task goes through every worker connection, under conditions fixed before any candidate runs. It follows the agent-evaluation method: fixed task and rubric, separate dimensions, budgets and stop rules, and only observed runs count.

- **Candidates.** Codex app-server (the baseline, already verified), Claude Code through ACP, Claude Code headless (the current adapter), Cursor ACP, Gemini CLI ACP, Grok ACP and Antigravity headless. Each records the tool version and the adapter version.
- **Task.** A synthetic fixture repository at a pinned commit. The task packet asks the worker to add one small function and its unit test, run the test, and commit on its assigned branch. One step writes a file and one runs a command, so a worker in its default mode is likely to ask. No push, no pull request and no network access beyond the worker's own model calls.
- **Isolation.** A fresh worktree per run from the pinned commit, the person's own install and sign-in, nothing imposed on the worker, and no secret or private data in the task.
- **Dimensions**, each scored pass, fail or not applicable, never summed:
  - start: readiness passes, the session opens in the requested work directory, and the version is recorded
  - streamed progress: at least one progress event reaches the progress line before the turn ends
  - permission routed: a worker request reaches the originating conversation's approval card and the answer returns to the worker. Not applicable when the worker's own mode asked nothing, recorded with that mode
  - cancel: a cancel during the long step ends the turn as interrupted within 10 seconds (P), with no tool call after it
  - resume after kill: the worker process is killed mid-turn; Core starts it once in the same worktree, loads the same session and the task finishes without repeating a committed effect
  - done detection: the completion event is typed done only when the commit exists and the test passes; a missing final result is a failure, never done
  - outcome: the test passes at the result commit, checked by running it
  - trajectory: the worker ran the test before claiming done and stayed inside its worktree
- **Budgets.** At most three runs per candidate: one, plus up to two reruns for an infrastructure fault, never for a model mistake. Fifteen minutes (P) per run.
- **Stop rules.** Two start failures in a row mark a candidate blocked. A candidate without a signed-in account is marked not observed.
- **Report.** Each run is marked observed, blocked or not observed, with its time and versions. The public record under `evidence/` holds only ids, codes, counts and versions. The recommendation is per worker (which connection to use) with a confidence level, and names the candidates that no other candidate beats on every dimension.
- **What it does not establish.** It does not rank models or code quality. It is not review, CI or merge evidence. It covers one host and one synthetic task.

The run that settled the largest open question is the [CC] ACP and headless comparison on the resume and permission dimensions, in Results.

## Results

The evaluation ran on 2026-10-23 with the candidates' own installs and
sign-ins; the record is [worker-evaluation.md](../../evidence/worker-evaluation.md).
Six candidates were observed and Gemini CLI was not, because it is not
installed on the host.

| Connection | Start | Progress | Permission | Cancel | Resume | Done | Outcome |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Codex app-server | pass | pass | pass | pass | pass | pass | pass |
| [CC] ACP | pass | pass | pass | pass | pass | pass | pass |
| [CC] headless | pass | pass | fail | n/a | n/a | pass | fail |
| Cursor ACP | pass | pass | pass | pass | pass | pass | pass |
| Grok ACP | pass | pass | n/a | pass | pass | pass | pass |
| Antigravity headless | pass | pass | fail | fail | n/a | fail | fail |
| Gemini CLI ACP | not observed | not observed | not observed | not observed | not observed | not observed | not observed |

[CC] through ACP answered the open question and stays the connection for that
worker: it routed approvals to the caller and restored the same session after a
kill, where the headless adapter has neither channel and could not finish the
task. Grok's own mode approved both the write and the command without asking,
so its permission row is not applicable. Cursor and Grok were signed in on this
host, so their rows are now observed rather than documented.

## Limits

- One host and one person's setup, including that person's own model routing for both installed workers.
- Cursor and Grok had no account at the protocol probe, so only their handshakes were measured then; the evaluation above ran both with a signed-in account. Gemini CLI stayed uninstalled, so its row still relies on the documented behavior.
- Plans, sub-agent events, quota exhaustion and rate-limit errors did not occur in the probe runs.
- The probe ran three short prompts per worker. It shows that the protocol paths work, not how they behave under long tasks.

## Sources

Read on 2026-10-23.

- ACP: [specification and schema](https://github.com/agentclientprotocol/agent-client-protocol) (schema 1.26.0, Apache-2.0), [protocol site](https://agentclientprotocol.com), [agent registry](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json) (43 agents), [community libraries](https://agentclientprotocol.com/libraries/community), and the schema files inside npm `@agentclientprotocol/sdk` 2.0.0.
- Claude Code adapter: [claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp) 0.94.0, read in its npm package.
- Codex adapters: [agentclientprotocol/codex-acp](https://github.com/agentclientprotocol/codex-acp) 2.2.2, read in its npm package; [zed-industries/codex-acp](https://github.com/zed-industries/codex-acp) 0.16.0, archived.
- Codex: [app-server documentation](https://learn.chatgpt.com/docs/app-server).
- Cursor: [CLI ACP documentation](https://cursor.com/docs/cli/acp), [installation](https://cursor.com/docs/cli/installation).
- Gemini CLI: [ACP mode](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/acp-mode.md), [`--acp` flag change](https://github.com/google-gemini/gemini-cli/pull/21171), released in 0.33.0.
- Grok Build: [headless and ACP documentation](https://docs.x.ai/build/cli/headless-scripting), [repository](https://github.com/xai-org/grok-build), npm `@xai-official/grok` 1.0.68. Not the community `grok-cli`, which installs a command of the same name.
- Copilot CLI (`copilot --acp`), OpenCode (`opencode acp`) and Goose (`goose acp`) also offer ACP: [Copilot](https://docs.github.com/en/copilot/reference/acp-server), [OpenCode](https://opencode.ai/docs/acp/), [Goose](https://goose-docs.ai/docs/gdk/acp/). They would join as added agents.
- A2A: [specification](https://a2a-protocol.org/latest/specification/), [releases](https://github.com/a2aproject/A2A/releases), [roadmap](https://a2a-protocol.org/latest/roadmap/), [a2a-go](https://github.com/a2aproject/a2a-go), [a2a-cli](https://github.com/a2aproject/a2a-cli).

No code was copied from any of these sources.
