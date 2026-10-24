# Research: text-form tool calls and fresh-session recovery

A coding worker or model route sometimes ends a turn with `stopReason="stop"` (or `end_turn`) and no structured `toolCall`, while its assistant text contains a serialized tool call such as `[tool_use id=toolu_… name=_bash]` followed by JSON arguments. Because no real tool call was emitted, the harness or loop treats the turn as finished and the worker stalls mid-task.

This note records what the 2026-10-24 `omp` and InferHub runs (`omp-parse`, `inferhub-opt`) measured across the work fleet, what caused the repeat loop inside a session, what fixed it with before-and-after numbers, and the replay evaluation that must pass before the recovery path is wired. It is evidence for [#1611](https://github.com/lina-system/lina/issues/1611) and feeds the contract rules in [work-and-delegation.md](../design/work-and-delegation.md#adapter-boundary), [work-and-delegation.md](../design/work-and-delegation.md#stall-and-failure) and [go-core.md](../design/go-core.md#model-path-and-accounts). It is not normative; the design contracts in [docs/design](../design/) stay the source of truth.

Raw session transcripts, host names and provider request identifiers stay private outside the repository. Only counts, rates, format patterns and synthetic public test cases appear here.

## Result

1. **Why same-session nudging failed.** In clean sessions of `omp` 18.12.5 on `inferhub/deepseek-v4.1-flash`, the first text-form tool call appeared in about **1 in 5,200 assistant turns (0.019%)**. Once a single text-form tool call entered the session transcript as assistant text, **145 of 459 subsequent turns (31.6%)** in the same session also emitted text-form tool calls. Nudging the worker inside the same session left the contaminated assistant turn in context, so the model copied its own prior text formatting on roughly one turn in three.
2. **Why model/request tuning was not the fix.** Replaying a contaminated 40-turn session directly against InferHub without `omp` reproduced the fault on both `anthropic-messages` (**4 / 211 replays, 1.9%**) and `chat-completions` (**2 / 205 replays, 1.0%**). Replaying the same session from an earlier clean point before the first text-form tool call gave **0 / 16 (0%)**, and a short synthetic probe gave **0 / 8 (0%)**. Varying request settings (`temperature`, `tool_choice`, streaming) on a contaminated transcript did not stop the repeat loop; removing the contaminated history did.
3. **One rule in Core.** A turn that ends with a text-form tool call and no real tool call is detected by Core, never shown as progress or a final reply, and handled by:
   - **(1) Parsing into a real call** when the arguments are complete valid JSON (or complete named parameters) and the tool is allowed in the active declaration set (matching the declared name, or the declared name after stripping one leading `_`). The call runs through Core's normal authorization and tool dispatch, and the raw text-form call is never recorded as assistant prose.
   - **(2) Starting a fresh session on the same worker or model route** with a short state summary when the arguments are incomplete or malformed, or the tool cannot be dispatched by Core. The contaminated session transcript is never resumed or loaded (`session/load` and `thread/resume` are not used for it), there is no same-session nudging loop, and there is no silent hand-off to another worker.

## Method and sample

- **Fleet sample (2026-10-24).** 40 affected worker sessions running `omp` 18.12.5 on `inferhub/deepseek-v4.1-flash` across seven Linux and macOS work hosts, plus a complete 6-hour single-host window covering 16 sessions and 4,293 assistant turns.
- **Client and parser check (`omp-parse`).** Inspection of `omp` 18.12.5's response handling and `tools.format` setting against recorded session JSONL files (`~/.omp/agent/sessions/…/*.jsonl`) to verify whether the client mis-parsed structured tool calls or received plain text from upstream.
- **Direct wire replay (`inferhub-opt`).** Direct HTTP replay of a recorded 40-turn contaminated session against InferHub (`anthropic-messages` and `chat-completions`), comparing the full contaminated prefix against an earlier clean prefix and a short synthetic prompt under varied `temperature`, `tool_choice` and streaming settings.
- **Intervention comparison.** Before-and-after measurements across four interventions deployed on 2026-10-24: same-session nudging with 4-strike cross-worker takeover, deterministic call parsing, pinning the pre-compaction context threshold to 200,000 tokens, and restarting stalled sessions in a fresh session on the same worker with a short state summary.

## Text tool-call formats seen

In every recorded case, the upstream response arrived with `content[].type="text"`, `stopReason="stop"` and no `toolCall` item. Five text-form serialization families appeared in the assistant text:

| Format family | Example pattern in `content[].text` | Arguments | Where seen |
| --- | --- | --- | --- |
| Bracketed `tool_use` header + JSON line | `[tool_use id=toolu_00_… name=_bash]` followed on the next line by `{"command":"go test ./..."}` (or bare `[tool_use`) | Complete JSON object on the next line when not cut off | Dominant format on `inferhub/deepseek-v4.1-flash` across all hosts |
| XML `<tool_use>` block | `<tool_use id="…" name="…">{"command":"…"}</tool_use>` | JSON object or XML child tags inside `<tool_use>` | `omp` sessions after long context or compaction |
| XML `<function_calls>` / `<invoke>` | `<function_calls><invoke name="…"><parameter name="…">…</parameter></invoke></function_calls>` (or bare `<invoke`) | Named `<parameter>` elements | Anthropic-style prompted fallback leakage |
| `antml:`-prefixed XML | `<antml:function_calls><antml:invoke name="…">…</antml:invoke></antml:function_calls>` | Named `<antml:parameter>` elements | Anthropic XML dialect leakage |
| XML `<tool_call>` wrapper | `<tool_call>{"name":"…","arguments":{…}}</tool_call>` | JSON object with `name` and `arguments` | OpenAI / Qwen / DeepSeek chat-template leakage |

**Leading-underscore tool names.** In `omp` sessions, text-form tool calls frequently prefixed the tool name with an underscore (`_bash`, `_read`, `_edit`, `_grep`), matching how `omp` formats earlier tool invocations when rendering history back into text. When a worker was nudged inside the same session, it sometimes emitted a real structured `toolCall` on the next turn that kept the underscore (`name: "_bash"`), which failed immediately with `Tool "_bash" is not available`. Normalizing a single leading underscore is safe only when the exact name is not in the active tool declarations and the underscore-stripped name is declared.

## Onset and contamination rates

| Measurement | Sample | Observed count | Rate |
| --- | --- | --- | --- |
| Clean-session first onset | Fleet-wide clean assistant turns before any text tool call in the session | ~1 per 5,200 turns | **0.019%** |
| Post-onset repeat rate in the same session | 459 assistant turns following the first text tool call across 40 affected sessions | 145 text-tool-call turns | **31.6%** |
| 6-hour single-host session incidence | 16 worker sessions on one host over 6 hours | 13 sessions with ≥1 text tool call | **81.3% of sessions** |
| 6-hour single-host turn incidence | 4,293 assistant turns across those 16 sessions | 555 turns with text tool calls; 354 with `stopReason=stop` and no real `toolCall` | **12.9% of turns** (8.2% terminal stalls) |
| Upstream offer split (`msg_<uuid>` vs `msg_01…`) | ~1,010 turns on `inferhub/deepseek-v4.1-flash` after 00:50 UTC on 2026-10-24 across 4 hosts | `msg_<uuid>`: 78 / ~280 turns; `msg_01…`: 0 / ~730 turns | **27.9%** vs **0.0%** |
| Single-session contamination examples | Two review sessions (`LN-1082R`, `LN-1496R`) | `LN-1082R`: 15 text calls vs 31 real calls; `LN-1496R`: 3 text calls in 64 lines across 2 manual nudges | **32.6%** of tool attempts in `LN-1082R` |

**Context-length onset distribution.** Across the 13 affected sessions in the 6-hour host window, most first occurrences happened once input context reached **280,000 to 450,000 tokens**, when long tool outputs and compaction summaries filled the prompt. A smaller tail of first occurrences happened at **13,000 to 59,000 input tokens** on the `msg_<uuid>` upstream offer.

## What was tested and what fixed it

| Intervention | What was changed | Before | After | Verdict |
| --- | --- | --- | --- | --- |
| **1. Fresh session restart on the same worker** | On an unrecoverable text-form tool call, end the contaminated session, keep the worktree and branch, and start a fresh session on the same worker with a short state summary (never loading the old session transcript). | Same-session nudging left the text call in history: **145 / 459 (31.6%)** of later turns repeated the fault, and 5 tasks escalated to another worker after 4 nudges. | Fresh session resets the history to the clean baseline (**~1 in 5,200 turns, 0.019%**); **0 tasks** handed off to another worker. | **Adopted** as step (2) of the Core rule. |
| **2. Parser mode (`omp-parse`)** | Checked `omp` 18.12.5 native tool parsing and `tools.format`; tested deterministic extraction of complete allowed calls before restarting. | `omp` 18.12.5 has no text-tool fallback parser in native mode (`tools.format` only switches the whole session to prompted mode), so 100% of terminal text-tool turns stalled or needed a restart. | Complete JSON calls for declared tools (with `_name` normalized to `name` when declared) execute directly as real calls without recording raw `[tool_use …]` prose; only incomplete or unallowed calls need a fresh session. | **Adopted** as step (1) of the Core rule. |
| **3. Compaction threshold** | Pinned `compaction.thresholdTokens` to **200,000 tokens** (`DV_OMP_COMPACT_TOKENS=200000`) instead of letting sessions grow to the model window ceiling. | Unpinned sessions grew to **280,000–450,000+ input tokens**, where onset clustered (13 / 16 sessions affected in 6 hours; 354 stalled turns). | Sessions compact at **200,000 input tokens**, staying below the 280k–450k high-onset band. | **Adopted** for worker and model-route compaction ceilings; complements [Compaction and resume](../design/conversation-and-memory.md#compaction-and-resume). |
| **4. Request settings and wire surface (`inferhub-opt`)** | Replayed the 40-turn contaminated transcript and clean prefixes directly against InferHub on `anthropic-messages` and `chat-completions` with varied `temperature`, `tool_choice` and streaming. | Contaminated 40-turn replay failed on `anthropic-messages` (**4 / 211, 1.9%**) and `chat-completions` (**2 / 205, 1.0%**) regardless of request parameter tweaks. | Earlier clean cutoff in the same session: **0 / 16 (0%)**; short synthetic probe: **0 / 8 (0%)**. | **No model/request tuning.** Keep contaminated turns out of history instead of tuning provider parameters. |

### Why fresh-session restart beats nudging and worker hand-off

- **No nudging loop.** Sending "please use real tool calls" into the same session adds another turn on top of the contaminated assistant message. Because the contaminated message remains in the prompt prefix, the model continues to imitate it on 31.6% of subsequent turns.
- **No silent hand-off to another worker.** Handing the task to a different worker after N nudges threw away an otherwise healthy, cheap worker because of prompt contamination that a fresh session on the same worker clears immediately. Worker hand-off stays reserved for quota, rate-limit or sign-in blocks ([work-and-delegation.md](../design/work-and-delegation.md#stall-and-failure)).
- **Short state summary, never transcript reload.** Calling `session/load`, `session/resume` or `thread/resume` after a text-form tool call reloads the contaminated assistant turn into the model context and re-triggers the 31.6% repeat rate. The fresh session starts with `session/new` (ACP), `thread/start` (Codex) or a fresh continuation request (LINA model route) carrying only:
  1. the original task packet or brief;
  2. the current worktree branch, `git status` / `git log --oneline -5` or verified tool receipts so far;
  3. a short rule reminder: use structured tool calls only, never prefix tool names with `_`, and do not read prior session transcript files.

## Evaluation, defined before building

Before wiring recovery into `kit/loop`, `turn`, `runs` or `worker`, a deterministic replay evaluation scores the detector, parser and recovery action on recorded and synthetic transcripts.

### Replay set

The replay runner reads a UTF-8 JSON Lines file (one turn case per line). Follow the same split as [message-intake.md](message-intake.md#replay-evaluation):

- **Private real set (outside the repository).** Recorded assistant turns extracted from the 2026-10-24 sessions:
  - terminal text-form tool calls across all five format families (`[tool_use …]`, `<tool_use>`, `<function_calls><invoke>`, `<antml:invoke>`, `<tool_call>`), both with complete valid arguments and with cut-off or malformed JSON;
  - leading-underscore tool names (`_bash`, `_read`, `_edit`, `_submit_receipt`) and undeclared tool names;
  - normal prose turns from the same sessions, including markdown code blocks, diffs, review reports and documentation that quote `[tool_use` or `<invoke>` literally;
  - turns that contain both a real structured `toolCall` and prose mentioning tool calls;
  - reasoning / thinking blocks that mention tool syntax before a normal prose reply.
- **Public synthetic set (committed in the repository for CI).** Self-contained synthetic cases covering every format family, `_`-prefixed tool names, truncated JSON, disallowed tools, fenced code blocks discussing `[tool_use id=…]`, thinking blocks, and mixed real-plus-text tool calls.

Each case carries:

- `id`, `slice` (`complete_allowed`, `incomplete_or_disallowed`, `normal_prose`, `real_tool_present`), and `route` (`model_route`, `acp_worker`, `codex_worker`);
- `declared_tools`: the tool names and JSON Schemas offered in that turn;
- `turn`: `stop_reason`, structured `tool_calls` (empty or non-empty), assistant `text` and `thinking` blocks, and streamed delta timestamps;
- `gold`:
  - `detected`: whether the turn ended with a text-form tool call and no real tool call (`true` or `false`);
  - `action`: `none` (normal turn), `parse_and_dispatch` (step 1), or `restart_fresh_session` (step 2);
  - `parsed_call`: when `action == "parse_and_dispatch"`, the expected canonical tool name (with leading `_` stripped when applicable) and canonical JSON arguments;
  - `progress_leaked`: must be `false` on every positive case (the raw text-form tool call is never emitted as visible progress or final reply text).

### Metrics and ship bar

The replay reports three primary metrics per slice and overall:

| Metric | Definition | Ship bar |
| --- | --- | --- |
| **1. Recovered-call accuracy** | On `complete_allowed` cases, the share where `action == "parse_and_dispatch"` and both the normalized tool name and canonical JSON arguments match `gold.parsed_call`. On `incomplete_or_disallowed` cases, the share routed to `restart_fresh_session` with zero tool executions. | **100%** on the public synthetic set; **≥ 98%** on the private real set, with **0%** execution of incomplete, malformed or disallowed calls. |
| **2. False positives on normal prose** | On `normal_prose` and `real_tool_present` cases (including prose, markdown code blocks or diffs that quote `[tool_use` or `<invoke>`, and turns that already carry a real `toolCall`), the share where `detected == true`. | **0.0%** (zero false positives on both synthetic and real sets). |
| **3. Time to recovery** | Elapsed handling time and extra model turns from the terminal event of the defective turn to recovery: either structured tool dispatch in the same turn (`parse_and_dispatch`, 0 extra model requests before tool execution) or opening the fresh session with the state summary (`restart_fresh_session`, 0 same-session nudges, 1 fresh session start). | **0 same-session nudges**; deterministic parse-or-restart decision in **< 5 ms** per turn in unit/replay runs; at most **1 fresh-session restart** before a repeated fault surfaces as a decision row. |

## Implementation issues

Filed in milestone `M4 Fit how each agent works`:

- [#1621](https://github.com/lina-system/lina/issues/1621) — Text tool-call replay and parser: score recovery and false positives before wiring.
- [#1622](https://github.com/lina-system/lina/issues/1622) — Model routes: recover complete text tool calls or continue in a fresh session.
- [#1623](https://github.com/lina-system/lina/issues/1623) — Workers: recover text tool calls or restart in a fresh session on the same worker.
