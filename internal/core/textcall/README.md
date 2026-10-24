# Text-form tool calls

`Detect` is the pure shared boundary for [#1621](https://github.com/lina-system/lina/issues/1621).
It proposes `none`, `parse_and_dispatch`, or `restart_fresh_session` from a
completed turn and its active tool declarations. It does not execute tools,
contact a provider, open sessions or write conversation state. Model routes install `Guard` in #1622; worker adapter wiring remains separate
work in #1623. `Guard` translates declarations to the shared parser and delegates
stream filtering and bounded continuation to `kit/loop.GuardTextCalls`.

The [model contract](../../../docs/design/go-core.md#model-path-and-accounts) and
[adapter contract](../../../docs/design/work-and-delegation.md#adapter-boundary)
own recovery behavior. The [research evaluation](../../../docs/research/text-tool-calls.md#evaluation-defined-before-building)
defines the metrics and ship bar.

## Parsing boundary

The parser handles bracketed `tool_use` plus JSON, XML `tool_use` with JSON or
child tags, `function_calls`/`invoke`, `antml:` invocations and wrappers, and
`tool_call` JSON wrappers. It accepts terminal `stop`, `end_turn`, `length` and
`max_tokens` turns. Any structured call preempts recovery. Text blocks concatenate
without invented separators; thinking blocks never enter detection.

A call starts on its own visible line, optionally after prose or a thinking block.
Fenced and indented code, block quotes, diff lines and thinking markup are ignored.
A complete call followed by explanatory prose is not a terminal call. The parser
does not infer a call from an inline mention of its syntax.

The exact declared name wins. Otherwise exactly one leading underscore may be
removed if that result is declared. Duplicate declarations, duplicate JSON keys,
duplicate parameters, malformed or incomplete arguments, schema failures and
multiple calls are refused as a whole. No partial batch is proposed. JSON
arguments must be objects. XML named values remain strings when the property's
schema type is string or unspecified; an explicit non-string type requires valid
JSON in that parameter. String whitespace is preserved. JSON inside `tool_use`
and `tool_call` wrappers retains shell operators without requiring XML escaping;
named XML values support XML entities and CDATA.

Schemas use the existing JSON Schema validator, with Draft 7 as the default and
external resource loading disabled. Local `$ref` values work. Recovery still
requires the caller's normal authorization and tool dispatch; declaration/schema
validation does not grant permission. Positive decisions return no visible text.
This is a projection contract, not proof of suppression in a running stream.

## Replay

From the repository root:

```sh
go run ./cmd/lina text-tool-replay internal/core/textcall/testdata/synthetic.jsonl
# An installed binary accepts any explicit case-file path:
lina text-tool-replay /path/to/private-cases.jsonl
```

Each UTF-8 JSONL line has these required fields:

| Field | Shape |
| --- | --- |
| `id` | Unique nonempty case identifier |
| `slice` | `complete_allowed`, `incomplete_or_disallowed`, `normal_prose`, `real_tool_present` |
| `route` | `model_route`, `acp_worker`, `codex_worker` (label only; no provider is called) |
| `declared_tools` | Array of `{ "name": "bash", "schema": { "type": "object" } }` |
| `turn.stop_reason` | Nonempty stop reason; only the terminal reasons above permit recovery |
| `turn.tool_calls` | Array of structured `{ "name": "...", "arguments": { ... } }` objects; additional provider fields may be retained |
| `turn.text`, `turn.thinking` | Arrays of text blocks in wire order |
| `turn.delta_timestamps_ms` | Nonnegative, nondecreasing elapsed millisecond offsets; an empty array means no recorded deltas |
| `gold.detected` | Boolean |
| `gold.action` | One of the three actions |
| `gold.parsed_call` | Required only for dispatch: canonical declared `name` and object `arguments` |
| `gold.progress_leaked` | Explicit `false` |

The committed [synthetic corpus](testdata/synthetic.jsonl) supplies complete
examples. JSON object order is irrelevant, and equivalent numeric values compare
equally without converting large integers to floating point. Unknown case fields,
missing fields, inconsistent labels, invalid schemas and duplicate case IDs are
rejected. Blank lines count as `missing_line`. Lines over 1 MiB count as
`line_too_large`; the reader drains that line and continues. Empty files, missing
files and read errors are explicit failures. A deleted case cannot be inferred
without an external manifest; corpus tests check the required slices.

Output contains overall and per-slice recovered-call accuracy and false-positive
rates, with numerators and denominators. A rate with no eligible cases is `null`.
Invalid rows remain in `rejection_reasons` and `results`, outside metric
denominators, and always fail the run. Each valid case records its match result.
No raw text, thinking, arguments, schema or underlying I/O error is printed.

`handling_ns` measures `Detect` from the assembled terminal input to its decision;
it includes parsing and schema checks, excludes file decoding and gold validation,
and does not reuse historical stream timestamps as a latency measurement. Totals,
maximum and the count at or above 5 ms appear in each score. The command measures
the research latency bar; unit tests do not pass or fail based on scheduling luck.

`simulated_dispatches` counts dispatch proposals. `planned_extra_model_turns` is
zero for dispatch and one for restart; `planned_fresh_session_starts` is one for
restart. There are zero same-session nudges. These are deterministic planned
costs, not measured provider turns or completed sessions. Runtime stream tests and the M4 headless scenarios separately exercise
repeated-fault failure and actual resumed tool dispatch; replay timings still
measure only the pure decision.

The command returns 0 only when at least one case was scored, every case matched,
and none was rejected; 1 for rejected or mismatched cases, 64 for invalid usage,
and 74 if the report cannot be written. A passing subset does not establish full
format coverage or the private corpus's accuracy. Private transcripts stay outside
the repository; no private-corpus score is claimed by the synthetic suite.
