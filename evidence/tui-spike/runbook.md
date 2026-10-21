# Owner terminal runs: S1–S11

The [bounds are already declared](https://github.com/thisisjun786/lina/issues/87#issuecomment-6307893344).
The [terminal list](terminals.json) is **proposed, pending owner confirmation**:
macOS Apple Terminal, the installed LINA OS default terminal, and Apple Terminal
over SSH. Resolve the actual LINA OS terminal product and versions when confirming
the list. No person has performed these runs yet, and no final library step is
selected. [Follow-up #371](https://github.com/thisisjun786/lina/issues/371) owns these
remaining cases and the final decision. They do not block LN-85's harness delivery;
this runbook does not close #87 or establish terminal acceptance.

The commands use the [scenario runner](../../internal/tui/spike/run/terminal_test.go)
and LN-86's recording writer. S8 runs its headless harness. The other commands
write to the real terminal and record its output through the same emulator.
An unsuccessful bound is saved as a failure, or S7's documented gap, rather than
silently changed. Use synthetic text only.

## Set up once on each terminal

Run from this checkout's repository root in Bash or Zsh. The binary runs on Linux
and macOS; build it on the application side of an SSH session. The terminal
metadata still names the terminal application on the local device.

```sh
go test -count=1 ./internal/tui/spike/...
SPIKE_OUTPUT=$(mktemp -d "${TMPDIR:-/tmp}/lina-spike.XXXXXX")
SPIKE_RUNNER="$SPIKE_OUTPUT/scenarios"
go test -c -o "$SPIKE_RUNNER" ./internal/tui/spike/run
SPIKE_REVISION=$(git rev-parse HEAD)
SPIKE_TERMINAL='Apple Terminal'
SPIKE_VERSION='REPLACE with the actual terminal version'
SPIKE_TRANSPORT=local
SPIKE_CONFIRMED=false
SPIKE_RUN_NUMBER=0
spike() {
  SPIKE_RUN_NUMBER=$((SPIKE_RUN_NUMBER + 1))
  "$SPIKE_RUNNER" -scenario "$1" \
    -terminal "$SPIKE_TERMINAL" -terminal-version "$SPIKE_VERSION" \
    -transport "$SPIKE_TRANSPORT" -confirmed="$SPIKE_CONFIRMED" \
    -revision "$SPIKE_REVISION" \
    -evidence "$SPIKE_OUTPUT/$1-$SPIKE_RUN_NUMBER.json"
}
```

Replace the product and version with observations from the terminal's About
screen or version command; for SSH set `SPIKE_TRANSPORT=ssh`. `TERM` is read
directly from the application environment. After the owner confirms the matrix,
set `SPIKE_CONFIRMED=true`. Real-terminal commands refuse to run while it is false.
S8 can run before confirmation. Keep the same checkout and pins used to build the
binary; rebuild after a source or dependency change.

Start at **140 columns ×45 rows** (`stty size` prints rows then columns). Leave the
terminal idle during automatic measurements. Ctrl+D completes interactive
screens, except S11, which completes after its final answer. Ctrl+C cancels without
writing a record. SIGTERM also cancels through terminal restoration. In S7, where key decoding is intentionally obstructed, the
command cancels itself after its one-second observation.

## One command per scenario

| Command | What to do | Typical duration |
| --- | --- | --- |
| `spike S1` | Activate the Korean input method. Compose `글` after the initial `한`; inspect both uncommitted composition and the cursor. Ctrl+N moves to the mid-line case; compose `글` there. Ctrl+N moves to the wrap case; compose `글`, then Ctrl+D. Answer the cursor and composition questions for all three cases. | About a minute |
| `spike S2` | Keep the terminal exactly 140×45. Ten status-only ticks are measured after the full Hangul initial paint. Do not resize. | 10 seconds |
| `spike S3` | Six synthetic workloads stream automatically at a scheduled 250 events/s. After each, inspect early/middle/late per-flush costs and answer whether cost stayed flat. Numerical bounds are evaluated automatically. | About a minute |
| `spike S4` | Compare the tab reference. Resize at least three times, for example 140×45 →120×40 →80×30 →140×45. Let each final frame appear, then Ctrl+D. Compare the restored tab reference and answer the alignment question. | About a minute |
| `spike S5` | Observe the own DECRQM probes, an OSC 8 synthetic link and an explicit OSC 52 write of `LINA-SPIKE`. Check the link destination without opening it; explicitly paste into a disposable scratch area to check the write. Answer the support and leaked-reply questions. The command never reads the clipboard. | Under a minute |
| `spike S6` | Press and hold `t` (Toggle), `e` (Edit), and `a` (Approve); inspect the action counts. Ctrl+N switches to plain-key mode. Repeat the key checks, then press bare Esc at least five times and Ctrl+D. Answer whether the gate behaved correctly. | About a minute |
| `spike S7` | No typing is needed. The runner injects a paste start without an end marker, then `x`, through the library's input decoder. A recovery miss is recorded with the required mitigation. | About a second |
| `spike S8` | No terminal interaction. Runs the LN-86 headless recording writer through Hangul, paste, sealed Glamour output and quit, and writes the measured record. | Under a second |
| `spike S9` | Leave the 200-item window untouched. CPU time, output bytes and resident memory are measured after its first paint; the renderer ticker remains enabled. | 15 seconds |
| `spike S10` | Inspect Glamour and own-renderer tables in each available width mode, then Ctrl+D and answer the alignment questions. The mode is selected before painting. A permanently enabled mode 2027 permits only grapheme observations; unsupported grapheme mode permits only wcwidth. Either missing mode fails the comparison. The original mode is restored. | About a minute |
| `spike S11` | With at least 140 columns and 16 rows, inspect each displayed width from 100 through 140. `y` or `n` records usability and advances one column. After the final width, explain why the first usable width works, or why none does. Every width at and above the threshold must remain usable for a pass. | About a minute |

The complete [scenario plan](scenario-plan.md) explains the observations and
unchanged bounds. A blank or unknown answer aborts the record; do not answer yes
just to finish. Commands print the saved result and filename. A failing measured
scenario still exits successfully after saving its evidence; a setup, input or
record-validation error exits nonzero and creates no result file. Reruns get new
filenames, and the writer refuses to replace existing evidence.

## Validate and return the records

```sh
"$SPIKE_RUNNER" -check-evidence "$SPIKE_OUTPUT"
```

The check uses `spike.ReadRecord`, verifies the declared bounds and recomputes each
result from its required measurements. It rejects missing fields, invalid scalar
metrics, changed bounds and inconsistent results. It does not establish the
truth of an observation or a complete terminal/scenario matrix. Keep every JSON
file, including failures and gaps; return them with the confirmed terminal list.
Do not include shell histories, hostnames, addresses or personal text. Remove the
temporary binary after copying the records:

```sh
rm "$SPIKE_RUNNER"
```

## Measurement limits and decision

- S3 retains raw build, flush, key-latency and arrival samples as JSON strings in
  scalar metric fields. Percentiles use nearest rank. Key latency includes the
  input queue backlog and library processing, starting at an injected application
  key event; physical keyboard transport is observed in S6. Heap is sampled after
  each input event and final rendering. The recorder's retained output contributes
  to measured heap. Flat cost remains an explicit owner judgment from the samples;
  no new numeric tolerance replaces that bound.
- S5 mode probes use a 180 ms deadline inside the fixed 200 ms bound. The reader
  consumes late reports for a further 500 ms. OSC 52 and OSC 8 have no write
  acknowledgements: their recorded times are output-write durations, with support
  observed separately. If a later reply reaches the shell, retain that observation
  as a miss; the finite drain does not prove that no arbitrarily late reply can
  arrive. The synthetic clipboard marker replaces the current clipboard content.
  Both probe timeout observations are required: either timeout fails S5 even
  within the 200 ms bound. An explicit unsupported-mode reply remains a resolved
  probe; its support observation stays false in the record.
- S7's mitigation describes missing input-decoder recovery. Cancelling this spike
  restores the terminal; it does not implement that mitigation in the product.
- S9 reports the process resident high-water mark, conservatively including
  initialization, and CPU as a percentage of one core. It does not divide by the
  number of available cores.
- S10 exercises a spike-only table candidate. Choosing it in a record does not
  wire it into the production TUI. The same is true of the S6 gate, S4 reflow
  adapter and S11 pane fixture.

These commands prepare observations; they do not select or accept a library step.
Once the owner runs the confirmed matrix, inspect S1–S11 together, keep all misses,
and evaluate F1, F2 and F3 in T-08 order without loosening a bound. Update T-08/T-09
when the resulting choice changes behavior. #87 remains open until its actual
Done when is met.
