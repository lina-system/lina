# M4-4 measurement runner

The per-agent tailoring of #589 is measured, not reviewed. The runner is
`lina method-replay <cases.jsonl> --changed <agent[,agent]>`; the contract is
[The runner](../docs/design/preset-work-methods.md#the-runner) and
[Building a case set](../docs/design/preset-work-methods.md#building-a-case-set).
This note records what was observed and what the repository does not establish.

## What the runner does

It reads one private case file, plays each case once per variant through the
configured model route and writes a JSON report. It is not installed behavior: no
conversation, schedule or turn calls it, and it performs no effect, because a
case's only output is reply text.

The two variants are the tailored triple the binary ships and the untailored one.
By default the untailored side is the shipped persona layers with the empty
work-method record, which isolates the record's own contribution; `--baseline
<dir>` takes a whole untailored triple, laid out like `persona/`.

Each case is scored by the mechanical checks it declares: `length` against its
budget, `no_question` on a reversible step, `no_internal` for a retired alias,
internal key, digest, id or host path, `done_evidence` for a done claim without an
evidence or not-verified clause, and `language` on every visible prose line.
Fenced code blocks are not scored as prose, and an empty reply fails every check.
For each agent the report gives baseline and tailored passes, the ids that passed
before and fail after as regressions, the ids that failed before and pass after,
and a verdict (`improved`, `unchanged`, `regressed`, `worse`, `insufficient`). It
holds case ids, check outcomes and counts only, so it can be committed.

## The observed run: the M4-1 replay set

The M4-1 set is private and stays outside every repository: 583 rows in 9 files,
one per agent role plus the coding workers. It was converted to 159 playable agent
cases; the converter, the case file, the reports and the raw extracts stay with
the private data, and no case or reply text entered this repository.

The 159 cases: system 26, dev 30, knowledge 25, lina 22, life 20, creative 20,
legal 11, investing 5. 136 are accepted replies marked must-not-change and 23 are
corrections. All are Korean; reply types are status 140, comparison 18 and
document 1. Declared checks: `language` 139, `no_question` 115, `no_internal`
104, `length` 71. No case declares `done_evidence`, because no converted case
carried a done claim. 172 turns, none injected.

`--repeat 3`, one model, one route, two comparisons: the record alone (shipped
layers with the empty record as baseline) and the whole M4-3 change (the pre-M4-3
persona snapshot as baseline).

| Agent | Cases | Record alone: base → tail | Verdict | Whole M4-3: base → tail | Verdict |
| --- | --- | --- | --- | --- | --- |
| system | 26 | 61 → 62 | regressed | 57 → 57 | regressed |
| lina | 22 | 60 → 58 | regressed | 58 → 58 | unchanged |
| dev | 30 | 59 → 60 | regressed | 64 → 60 | regressed |
| knowledge | 25 | 63 → 61 | regressed | 62 → 61 | worse |
| life | 20 | 31 → 30 | worse | 31 → 32 | regressed |
| creative | 8 / 13 | 21 → 23 | improved | 35 → 33 | regressed |
| investing | 0 | 0 → 0 | insufficient | 0 → 0 | insufficient |
| legal | 0 | 0 → 0 | insufficient | 0 → 0 | insufficient |

Regressions, record alone: `system-009`, `lina-012`, `dev-007`,
`knowledge-001`, `knowledge-011`. Whole M4-3: `system-018`, `dev-007`,
`life-014`, `creative-014`. Improvements, record alone: `system-015`,
`system-026`, `dev-019`, `creative-002`; whole M4-3: `dev-019`, `knowledge-015`,
`life-013`.

Passes per check, record alone, baseline → tailored: `length` 157/186 → 160/187,
`language` 339/342 → 341/343, `no_internal` 252/255 → 252/255, `no_question`
208/279 → 206/279. Whole M4-3: `length` 157/186 → 158/187, `language` 350/351 →
349/352, `no_internal` 261/264 → 261/264, `no_question` 212/285 → 207/286.

Ship rule: `ships: false` in both comparisons. No agent improves without another
regressing, and five to six roles regress in each. Under the design's rule the
M4-3 tailoring does not ship.

## What this change does with the result

The reply-shaping tailoring is reverted in this pull request: every preset drops
its `reply`, `ask`, `fill` and `done` values and the M4-3 persona-layer edits,
while keeping the fields that wire the mechanism (`tools.declare_first`,
`workers.dispatch` and `handoff.owns`). A replay request declares no tools and
creates no worker task, so this runner cannot measure those fields; they are kept
rather than reverted, and removing them would leave coding work undispatchable.
`docs/design/preset-work-methods.md` records that the reply-shaping values are
measured but not in effect, and release validation expects the mechanism-only
records. The finding returns to M4-2. The mechanism — the record, its loading,
dispatch, reply shapes, ask settings, done evidence, hand-offs and names — stays.

## Decisions

1. **A case declares only checks it can evidence.** An accepted case declares only
   checks its accepted reply passes; a correction case only checks its drawing
   reply fails. A check the owner's own accepted reply fails would mark the
   behaviour he accepted as a failure, whatever the model writes. A pair that
   meets neither rule is left out and counted, not scored.
2. **No judge.** An accepted reply is not compared with a model's by similarity:
   there is no qualified judge in the repository, and an unqualified one is not
   evidence; the five mechanical checks are what the design names.
3. **The result is reported and the change is reverted.** The report holds ids,
   check outcomes and counts, so it can be committed while the case text stays
   private; the revert is in the same pull request, as the issue's rule requires.
4. **`--changed` is required, and the report names unobserved agents.** A report
   with no changed agent has nothing to judge; starters without scored cases are
   listed as unobserved or insufficient, so their absence is visible rather than
   read as a pass.

## Limits

- The checks are text measures, not a judge. They cannot score whether a reply was
  right, only whether it broke a stated expectation; a reply can pass every check
  and still be wrong.
- Absolute pass rates are low: the model writes about 450 characters per status
  reply against the design's 300-character budget, and about half the Korean cases
  draw an English prose line. The before/after difference is therefore small
  beside the level.
- `no_question` and `done_evidence` use marker lists in
  `internal/core/methodreplay/score.go`; a phrasing outside them is not detected.
  `language` ignores lines with fewer than ten letters.
- Investing (5 cases) and legal (11) scored nothing in either comparison: every
  one of their cases was dropped as `model_error`, so they stay `insufficient` and
  this run establishes nothing about those two roles.
- Exclusions were 28 of 159 (record alone) and 23 of 159 (whole M4-3), all
  `model_error`. Creative shows 8 cases in one comparison and 13 in the other for
  that reason.
- The two sweeps used a binary built from `e4b337c4` with `vcs.modified=true`. Its
  tailored side carried the M4-3 records — both comparisons move, and a binary
  shipping the empty record would report `unchanged` — but that build was not
  reproduced from a clean revision.
- Record fields about tools, worker dispatch and hand-offs are not exercised by a
  replay request, so the runner cannot measure them at all.
- One model on one route at one time. It is not evidence about another model or a
  later revision.
