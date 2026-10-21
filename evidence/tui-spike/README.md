# TUI spike harness and evidence

This increment implements S8 of [T-08](../../docs/design/tui.md#library-choice-risks-and-spike-gate-t-08).
It pins Bubble Tea 2.1.0, Bubbles 2.2.1 (`textarea` only), Lip Gloss 2.0.6,
Glamour 2.0.1 (sealed blocks only), and Ultraviolet at
`v0.0.0-20261008173134-6b8d4baf91b4`. The exact module graph and checksums live
in [go.mod](../../go.mod) and [go.sum](../../go.sum).

[terminals.json](terminals.json) records the proposed real-terminal matrix.
Owner confirmation is pending; `confirmed_list` remains null until the owner
confirms it. This does not block #86. Confirm the list before #87's real-terminal
runs. Resolve the installed LINA OS default's product name at that point, and
capture each terminal's actual version and `TERM` in every run record. The
matrix covers macOS, LINA OS's default, and SSH, including Apple Terminal's
known probe differences. It contains no device names, addresses or user data.

## Running S8

```sh
go test ./internal/tui/term/... ./internal/tui/spike/...
go test -race ./internal/tui/term/... ./internal/tui/spike/...
go test ./internal/tui/spike/... -run '^TestS8$' -count=1 -v \
  -args -s8-evidence ../../../evidence/tui-spike/s8.json
```

The last command regenerates [s8.json](s8.json) after its assertions pass. It
runs the real Bubble Tea renderer at 20 by 6 cells with a recording writer,
feeds a synthetic Hangul/Latin composer a paste message, observes its parsed
cells and hardware cursor, then sends quit and joins terminal teardown. Bubbles
provides the textarea; Lip Gloss composes it with a sealed Glamour block. The
headless VT emulator is `github.com/charmbracelet/x/vt` at `v0.1.0`.

`term.Start` accepts a Bubble Tea model and explicit dimensions. `Send` injects
messages; `Output.Wait` waits for a predicate on a copied screen after a write.
`Wait` joins program exit; `Close` cancels and joins even on a failed scenario.
The caller must always defer `Close`. The recorder preserves raw bytes, write
counts, cell contents and widths, cursor position/visibility, and alternate
screen state. `Output.Bytes` and `Snapshot` return copies. Waiters wake on
writes or close; context deadlines only bound failure. No harness or scenario
uses sleeps, polling, renderer-byte goldens or fixed flush timing.

The harness disables terminal input and signal handlers, supplies its own
environment, and never opens a TTY. Emulator replies are discarded through a
closed response pipe so queries cannot block the output writer. This deliberately
measures output rendering only. The renderer's own ticker remains active; S8
does not establish the idle CPU or zero-output bound of S9. Recordings retain
all output in memory, so use finite synthetic scenarios only.

## Evidence format 1

Each JSON file is one complete scenario run. `spike.ReadRecord` validates its
required fields and rejects unknown fields, trailing records, missing values,
unknown scenarios/results, and unpinned library revisions.

| Field | Meaning |
| --- | --- |
| `format` | `1` |
| `scenario` | `S1` through `S11` |
| `terminal.name` | Actual terminal product, or explicitly named headless emulator |
| `terminal.version` | Observed terminal version |
| `terminal.term` | Actual `TERM` supplied to the application |
| `terminal.ssh` | Explicit boolean |
| `libraries` | Module path to exact version/revision; all six modules above are required |
| `bounds` | Nonempty map of declared bounds, recorded before running; metric names include units |
| `measured` | Nonempty map of observed values, with units in metric names |
| `result` | `pass`, `fail`, or `gap` |

Metrics are numbers, booleans or nonempty strings, never null. Use names such as
`flush_p95_ms`, `peak_heap_mib`, or `cursor_column_cells`. A qualitative bound
may be a string. The scenario producer checks its own bounds; structural
validation alone cannot prove a measurement or a passing result. A `gap` records
what was observed and its mitigation as explicit measured fields; it is never a
substitute for an unrun scenario. Missing fields and `not_run` are not evidence.

[s8.json](s8.json) is measured headless evidence. Byte and write totals are
observations from that run, not golden values or timing assertions. The test
checks the recorded library revisions against the current module pins. A pin
change must rerun S8 and replace its evidence.

## Decision and limits

Use the chosen Bubble Tea stack for the remaining spike, with `x/vt` as the
headless parser. The passing S8 run exercises all selected companion APIs
together. A handwritten escape parser was not selected because S8 needs screen,
wide-cell, cursor and alternate-screen semantics; stripping ANSI cannot prove
these. PTY-only tests cannot replace this deterministic output inspection.

This change adds test infrastructure and evidence, without wiring Bubble Tea
into `lina tui` or changing the T-08/T-09 product contract. S1–S7 and S9–S11,
real IME composition, probe replies, keyboard decoding, lost paste markers,
performance and idle bounds remain #87's work. No fallback is selected on the
strength of S8 alone. A real-terminal miss follows F1–F3 without loosening the
declared bounds.
