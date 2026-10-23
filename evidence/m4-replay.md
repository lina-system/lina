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

## One observed run

The M4-1 replay set is private and was not present on the host that built the
runner: no agent session store, no Honcho store and no file carrying `role_id` was
reachable from it. The owner-facing coding-worker logs were reachable, so this run
rebuilt the worker part of the set from them, outside every repository, and left
the eight agent roles unobserved. The case file is frozen by sha256; its builder
and the exact selection rules are recorded with the private data.

- 63 cases for the dev agent, all from owner-typed turns in Codex worker
  sessions: 60 accepted replies marked must-not-change, and 3 `done_claim`
  corrections.
- Every case declares only checks it can evidence: an accepted case only checks
  its accepted reply passes, a correction case only checks its drawing reply
  fails. Three correction pairs that showed no defect in the declared measures
  were dropped and counted, never scored.
- `--repeat 3`, one model (`gpt-6.1-sol`), one route; 0 cases excluded, 0
  rejected, 0 injected turns dropped.

| Comparison | Baseline | Tailored | Regressions | Improvements | Verdict | Ships |
| --- | --- | --- | --- | --- | --- | --- |
| The record alone (shipped layers, empty record as baseline) | 26 / 189 runs | 26 / 189 runs | `worker-codex-020` | `worker-codex-007`, `worker-codex-042` | regressed | no |
| The whole M4-3 change (pre-M4-3 snapshot `a4c25f15` as baseline) | 25 / 189 runs | 25 / 189 runs | none | none | unchanged | no |

Passes per check, record-alone comparison, baseline → tailored:

| Check | Passes |
| --- | --- |
| `length` | 36 / 177 → 37 / 177 |
| `language` | 91 / 186 → 97 / 186 |
| `no_internal` | 42 / 78 → 42 / 78 |
| `no_question` | 177 / 180 → 177 / 180 |
| `done_evidence` | 1 / 9 → 1 / 9 |

Seen: the runner plays a private case file, applies the five checks, summarizes
per agent and applies the ship rule; the report carries no case or reply text.
On this data neither comparison improves the dev agent, so under the design's
rule the M4-3 tailoring does not ship from this evidence, and the finding returns
to M4-2.

### Why the difference is this small

The three flips in the record-alone comparison sit on a threshold: one accepted
case's replies averaged 289 characters before and 296 after against a
300-character budget, and it lost one of three runs; two other cases crossed a
length or language edge the other way. A 63-case set at three repeats cannot
separate that from noise.

The dev record carries `tools`, `workers` and `handoff` and no `reply` field, and
a replay request declares no tools and creates no worker task. Those fields
therefore change the request only as text inside the `<work_method>` block; the
behaviour they govern is not exercised at all. The instructions and the reply-shape
paragraph are present in both variants, because the empty record is still a
record. What this method can measure is reply text, and for the dev agent the
substance of M4-3 is its layer text, which is the second row.

## Decisions

1. **A case declares only checks it can evidence.** Without that rule the same 63
   cases reported `improved` before calibration (23 → 27 passes) and `regressed`
   after it, because a check the owner's own accepted reply fails marks accepted
   behaviour as failure. The rule is now in the design contract.
2. **No judge.** An accepted reply is not compared with a model's by similarity:
   there is no qualified judge in the repository, and an unqualified one is not
   evidence; the five mechanical checks are what the design names.
3. **The result is reported, not reverted in this change.** Reverting the M4-3
   records and layer text would be a separate change across eight presets, and
   this run covers one role. The evidence and the finding go back to M4-2, and
   the record-level conclusion stays as measured.
4. **`--changed` is required, and the report names unobserved agents.** A report
   with no changed agent has nothing to judge; starters without cases are listed
   as unobserved, so their absence is visible rather than read as a pass.

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
- Seven of the eight agents are unobserved: this run says nothing about their
  tailoring, and the design forbids reading one role's cases as another's
  evidence.
- Record fields about tools, worker dispatch and hand-offs are not exercised by a
  replay request, so the runner cannot measure them at all.
- One model on one route at one time. It is not evidence about another model or a
  later revision.
