# Worker doing observations

Implements [#978](https://github.com/lina-system/lina/issues/978) and
[Progress line](../../../../docs/design/work-and-delegation.md#progress-line).

Reference: reason-machines/reason-network at `72ca2b3`, `src/doing.rs` and
`DESIGN.md` (MIT OR Apache-2.0). This is an independent Go implementation.

- Seen: title plus first in-progress todo; Claude `ai-title`, TodoWrite, task
  inventory; Codex `update_plan` string arguments and thread index; Gemini
  rewritten JSON; Cursor todo_write; appended-record cursors and a 120-character
  line. DESIGN also describes opencode titles/todos and lifecycle status.
- Inferred: Codex's typed thread/name and turn/plan notifications and ACP
  session_info_update/plan carry those same observations. The running adapters
  consume these directly; Gemini, Cursor, Grok, Claude ACP and opencode use ACP.
- Unknown: undocumented transcript variants and arbitrary third-party file
  locations. They supply no observation and cannot manufacture completion.

`testdata` contains synthetic content in those envelopes. Claude and Codex
shapes follow the reference; ACP and Codex native notifications follow the
vendored schemas. Gemini follows the reference's rewritten document shape.
Cursor's tool-call and opencode's event envelopes are compatibility fixtures;
installed execution for these workers uses the schema-backed ACP plan path.

The reader accepts named tool/metadata envelopes, never arbitrary recursive JSON
inside prose or tool results. Native streams are consumed once. `Tail` retains
an append offset and incomplete line, detects replacement/truncation, bounds a
read to 2 MiB and refreshes rewritten Gemini documents only on a change.
`ClaudeFiles` discovers only the bound session's transcript and reads changed
session task files, giving the latest task inventory precedence over TodoWrite.

Adapters attach a value snapshot to their existing event. Core validates the
thread, commits changed observations under the current run, and publishes Now
through the existing progress topic. A repeated busy state preserves Since;
idle does not establish completion. Old cards with no observation use the
existing activity classification. The UI does not infer worker progress.
