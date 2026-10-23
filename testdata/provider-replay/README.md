# Public provider replay

`fixture.json` is a fixed, synthetic workload. It contains the shipped Lina
persona, Core tool declarations, 40 user/assistant history pairs quoted from
public runtime documentation, and four live inputs. Inputs 2 and 4 ask for
`skill_index` and `skill_notices`; their results are fixed empty public catalogs.
`summary.segment` adds one compaction summary request over the first 40 history
items, shaped as Core sends it: the persona, the tool declarations and that
segment unchanged, then the summary instruction, with no current input and no
ContextPacket. Its report row is reported under the turn `summary`, so two
reports of this fixture compare the summary's plain tokens before and after a
change beside the conversation's own. No private text, account identifiers,
credentials or real tool effects are used.

The snapshot comes from `persona/lina/`, `docs/design/runtime.md`, the declaration
functions in `internal/core/exec/`, and Core's question, run, dispatch,
methodology, timer, worker and exchange declarations. It was captured against
`11360891` plus the capability dependency from #877, and the `summary` selection
was added with the cache-aware compaction change (#885). Keep the fixture unchanged
between measurements; a changed digest makes the comparison command refuse.
The snapshot is intentional, so changing production persona or tools does not
silently change the benchmark. Refresh it explicitly when changing workloads.

Start Core with the account route you intend to measure and
`LINA_QA_REAL_MODEL=1`. Run from a checkout, choosing a new output directory
outside any Git worktree:

```sh
LINA_QA_REAL_MODEL=1 lina provider-replay --provider chatgpt \
  --socket "$LINA_SOCKET" --fixture testdata/provider-replay/fixture.json \
  --out "$HOME/provider-before"
```

Use the configured adapter's name (`chatgpt` for the Responses route, or
`claude` for the Claude runtime). A different name is refused before sending.
`--model` and `--effort` optionally override the configured model and effort.
Core must already be running; this command does not discover credentials,
start Core, switch accounts or admit provider capabilities.

Run the same command after a change with another `--out` directory, then:

```sh
lina provider-replay compare "$HOME/provider-before/report.json" \
  "$HOME/provider-after/report.json"
```

To replay an episode export from the connected install, replace `--fixture`
with `--episode /path/to/episodes.jsonl`. All selected episodes pass Core's
current ownership, source, scope and removal checks. Recorded tool results are
used; replay never performs an effect. Reports use private file permissions and
refuse output inside a repository, including through a symlinked parent.

Reports list each adapter request by turn and round (the summary request is
`summary`, round 1), with input, cache read, cache write, writes by lifetime,
ordinary-rate input, and first output time in milliseconds. Unknown measurements are JSON `null` and display as `unknown`.
Timing starts before opening the transport and ends on its first nonempty text,
completed assistant text, or ready tool call; reasoning-only items do not count.
The report retains failures and the command exits nonzero for an incomplete run.
No prices or cost estimates are invented.

A native runtime can hide its internal HTTP requests behind one adapter stream;
this command reports the stream's usage as exposed by that adapter. It does not
invent per-request usage or lifetime buckets the runtime does not expose. The
loopback CI scenario checks the actual installed command, HTTP provider,
request counts, both tool loops, report, comparison and episode export input.
