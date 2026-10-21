---
name: meta
description: Use when recording or improving a reusable procedure as a skill.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Record a reusable procedure after using it, receiving a correction, or repeating
non-trivial work. Read the catalog and the closest existing skill first. Patch
that skill when its scope fits; create one only when none fits.

Use a folder with SKILL.md. Its front matter contains a stable lowercase name
and a one-line description saying when to use it. Use plain or quoted one-line
values for both fields. Put instructions below the closing front-matter marker.
State the trigger, scope, steps, pitfalls and how to verify the outcome. Keep
facts about the person in memory; keep preferences for this procedure here.

Read the current version before writing. Send only changed instruction Markdown
files to write_skill with that version. Core classifies the entire skill and
refuses automatic adoption if it contains executable files, tools, connections,
permissions or unsupported front matter. Such a change requires an explicit
proposal and work-engine implementation.

After write_skill commits, tell the person in one line what was learned and
retain its undo handle for undo_skill. Do not claim learning on a failed write.
A changed skill applies to subsequent turns. Undo creates a new version with
the previous text; do not propose the undone content again.
