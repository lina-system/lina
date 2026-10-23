# S1–S11 terminal run plan

This is a preparation document for [#87](https://github.com/thisisjun786/lina/issues/87),
not a measurement record or a library acceptance. The bounds come from
[T-08](../../docs/design/tui.md#library-choice-risks-and-spike-gate-t-08);
[T-09](../../docs/design/tui.md#rendering-width-and-korean-t-09) defines the rendering
and width behavior. Keep these bounds unchanged when recording a failure.

## Before measuring

Use the recording harness and evidence format from
[#86](https://github.com/thisisjun786/lina/issues/86). Record its exact source commit
and the library revisions actually built. Confirm the terminal list with the
owner: at least one macOS terminal, the default LINA OS terminal, and one terminal
reached over SSH. Include Apple Terminal in S5's compatibility observations.
Record the local terminal application and version for an SSH run, not just the
remote shell. Do not include account names, hostnames, addresses or personal data.

The bounds were [declared on #87](https://github.com/thisisjun786/lina/issues/87#issuecomment-6307893344)
before the runs. Preserve that reference and the source revision of this plan
with the observations.
Use synthetic text and a disposable shell for terminal-state and paste cases.
Restore terminal modes and stop the harness after each run.

For every scenario on every confirmed terminal, retain:

- Terminal name and version, operating-system version, literal `TERM`, and SSH
  true or false. Record dimensions, font and input method where relevant.
- Source commit and exact Bubble Tea, Bubbles, Lip Gloss, Glamour and Ultraviolet
  revisions, including the selected fallback's configuration.
- Declared bounds, units, fixture sizes and measurement method before the run.
- Measured values, sample counts, raw synthetic observations and per-scenario
  result. Missing measurements must remain explicitly unmeasured; they cannot
  establish a pass or a library choice.

Use the upstream record parser to validate the records. A JSON document parsing
successfully is insufficient if required fields or scenario coverage are missing.
Automated cell arithmetic and emulator observations support the real-terminal
observations; they cannot establish physical IME placement or pane usability.

## Fixed bounds and observations

| Scenario | Declared bound | Observations to retain |
| --- | --- | --- |
| S1 | Cursor column equals expected width in composed, mid-line and wrapped cases; IME text appears in place. | Expected and observed cursor row/column and composition placement for each case and width mode. |
| S2 | At most 2 KiB per status tick on a full Hangul screen at 140×45. | Bytes for each isolated tick after initial paint; maximum bytes and the terminal dimensions. |
| S3 | Build and flush p50 ≤4 ms, p95 ≤16 ms; flat per-flush cost; peak heap ≤64 MiB; key-to-byte p95 ≤50 ms at 250 events/s. | Separate build/flush distributions, cost by stream position, heap peak, and key-to-byte distribution for every fixture below. |
| S4 | Reflow ≤100 ms after the coalesce; no tab-stop misalignment, with 200 history messages. | Last resize, coalesce completion and final flush timestamps, retained history count, expected and observed tab columns. |
| S5 | Each capability resolves within 200 ms; no reply leaks to the shell; behavior recorded per terminal, including SSH and Apple Terminal. | Probe, timeout and late-reply outcomes; synchronized output, grapheme mode, OSC 52 write and OSC 8 behavior separately. |
| S6 | Press-only gate correct; Esc to action ≤80 ms. | Press/repeat/release outcomes for Toggle, Edit and Approve; plain-key fallback; Esc action latency. |
| S7 | Later keys work within 1 s after a paste with a missing end marker, or the gap is recorded with a mitigation. | Lost-marker input, recovery latency, later-key outcome and any gap with a concrete mitigation. |
| S8 | Recording writer runs, quits and parses with the emulator without any sleep. | Run/quit completion, parsed cells/cursor, exact command and whether any sleep was used. |
| S9 | Idle CPU ≤1 percent of one core; no bytes written; resident memory ≤80 MiB with a 200-item window. | CPU time delta and wall interval, output-byte delta, resident memory samples, item count and measurement window. |
| S10 | Hangul, Latin and emoji table aligns under both width modes; otherwise our own renderer draws all tables. | Expected and observed cell boundaries in both modes, plus Glamour and own-renderer outcomes. |
| S11 | Find the usable side-pane threshold between 100 and 140 columns; default 120. | Width-by-width usability observations and the selected threshold with the operator's reason. |

KiB and MiB mean 1,024 and 1,048,576 bytes. Keep individual failed samples and
report the maximum for a maximum bound. Do not average away a failed terminal or
workload. The measurement method, sample count and percentile calculation belong
in the run record; choose them before collecting measurements.

## Scenario procedures

**S1 — IME.** With the Korean input method active, compose synthetic Hangul at
the end of a line, insert it between existing Hangul and Latin text, and compose
across a wrap. Observe the hardware cursor and the uncommitted composition as
well as the committed text. Compare against the shared measurer's expected cell
positions. Record the active width mode; enable grapheme mode only after terminal
confirmation of mode 2027. A correct committed string alone does not pass this
scenario.

**S2 — Status writes.** Fill a 140×45 viewport with synthetic Hangul, complete its
initial paint, and record each subsequent status-only update at the output writer.
Exclude initial paint from tick byte counts, but retain it separately. Include
all control-sequence bytes in each tick. A screen with fewer visible Hangul rows
does not exercise this bound.

**S3 — Streaming.** Run each of these fixtures independently: 100 KB prose;
300 KB text without a blank line; 300 KB text without any newline; a 1,000-line
code fence; a 1,200-row table; and a 300-item list. Record actual UTF-8 byte counts
and logical sizes. Deliver 250 events per second and interleave identified key
events while the stream is active. Measure input-to-first-corresponding-output
latency, including queuing and output writing. Keep build and flush measurements
separate. Compare equal observation windows near the beginning, middle and end
to expose cost growth, and report the samples rather than substituting an
invented numeric tolerance for the contract's flat-cost requirement. Heap
measurement covers the whole run, including completion and final rendering.

**S4 — Resize and tabs.** Load 200 history messages before sending a sequence of
sizes through the supported layout tiers. Record coalescing separately from
reflow; the 100 ms bound starts after coalescing. Check final painted cells and
cursor, not just completion of a layout function. Use a synthetic tab-stop
alignment fixture before startup, during the run and after quit to expose any
terminal tab-stop changes.

**S5 — Capabilities.** Exercise LINA's own synchronized-output and grapheme-mode
probes with supported, unsupported, absent and late responses. Record resolution
times and check the shell after quit for escaped late replies. On each terminal,
observe an explicit OSC 52 write of a synthetic marker and an OSC 8 link to a
synthetic HTTPS destination. Never read the clipboard. Record unsupported
features and timeouts explicitly. Support observations do not decide whether the
product should expose clipboard copy; that remains the design's open question.

**S6 — Keys.** Exercise Toggle, Edit and Approve with press, repeat and release
events. Releases must have no effect; repeats may affect Edit only. Repeat the
test without progressive keyboard events, where delivered keys count as presses.
Measure bare Esc from arrival to its action, including any escape-sequence wait;
also send a complete escape sequence to check it is not treated as bare Esc.

**S7 — Truncated paste.** Send the bracketed-paste start and synthetic content,
omit its end marker, then send an identifiable later key. Measure until that key
works. If recovery exceeds one second, retain the gap and state the mitigation
and how it can be verified. A normal completed paste does not cover this case.

**S8 — Harness.** Run the #86 recording-writer test through initialization,
observable output, quit and emulator parsing. Use completion and I/O signals for
ordering, never a sleep. Retain the test result and final cell/cursor observations.
Run `go test ./internal/tui/spike/...` and record its exit status.

**S9 — Idle.** Populate a 200-item window and await completed initial paint.
Declare the idle observation interval before starting it. Keep the real status
and renderer behavior enabled. Measure process CPU usage as
`100 × CPU-seconds / elapsed-seconds`, without division by machine core count.
Count all output bytes during the interval and sample resident memory throughout;
retain the peak rather than only the exit value. A test with timers artificially
disabled does not establish the candidate's idle behavior.

**S10 — Tables.** Use one table containing Hangul, Latin, combining marks and
emoji sequences. Inspect column boundaries under wcwidth and confirmed grapheme
mode, including wrapping. Record Glamour's result separately from LINA's table
renderer. A Glamour failure selects the own-table renderer required by T-09;
passing another width mode cannot hide it.
Select the physical mode before painting any table. If mode 2027 is permanently
enabled, record only grapheme observations and a failed comparison with a
mitigation; do not label that screen as wcwidth.

**S11 — Pane.** Show the conversation, composer and a representative synthetic
result in the side pane. Inspect widths from 100 through 140, including 120 and
the boundaries around any proposed threshold. Record whether content and actions
remain usable and why. The operator chooses the threshold from those observations;
the existing 120-column default alone is not a measurement.
Every width must have an observation. A passing threshold is the first usable
width, and every width from it through 140 must remain usable.

## Library decision and completion

The owner-run cases and final library decision are tracked in
[follow-up #371](https://github.com/thisisjun786/lina/issues/371), separately from
LN-85's harness delivery. #87 remains open for its real-terminal acceptance.

Collect the complete scenario-by-terminal matrix before claiming acceptance.
Record the candidate's misses without increasing any bound. Evaluate fallbacks
in T-08 order: F1, Bubble Tea with its own probe, quiet spinner, lower streaming
frame rate and frame memo; F2, changed-row Ultraviolet renderer with synchronized
output; F3, tcell v3. Link the selected step to the measurements that justify it.
Preserve failed runs and verify the changed candidate before claiming it passes.
Keep S7's explicit gap-and-mitigation outcome visible in that decision.

When the evidence changes behavior, update T-08 and T-09 in the same pull request.
Run the spike tests, parse every evidence record with all fields present, and run
`node scripts/foundation.mjs`. These checks supplement the terminal observations;
their success cannot supply observations that were never made.
