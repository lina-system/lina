# M4-4 measurement runner

The per-agent tailoring of #589 is measured, not reviewed. The runner is
`lina method-replay <cases.jsonl> --changed <agent[,agent]>`; the contract is
[The runner](../docs/design/preset-work-methods.md#the-runner). This note records
what was observed while building it and what the repository does not establish.

## What was run

| Command | Result |
| --- | --- |
| `go test ./internal/core/methodreplay/` | pass: reader refusals, injected-turn dropping, the five checks, per-agent summary, ship rule, text-free report |
| `go test ./cmd/lina/ -run 'TestReplayVariants|TestBaselineKind'` | pass: the tailored triple comes from the shipped records; the default baseline is the shipped layers with the empty record; a snapshot directory is read and an empty one is refused |
| `go test ./e2e/m4/ -run TestMethodReplay` | pass: the built command plays a case file against a fixture model, reports the dev agent improved and the life agent unchanged, ships the change, refuses to ship an unchanged agent, and refuses an invalid case line with exit 1 |

A run of the same command against the synthetic fixture
(`internal/core/methodreplay/testdata/synthetic.jsonl`) with a fixture model that
answers long for the untailored record and short with an evidence clause for the
tailored one produced this report excerpt:

```json
{"selected": 6, "scored": 6, "excluded": 0, "dropped_turns": 0,
 "baseline": "shipped-layers-without-record", "variants": ["baseline", "tailored"],
 "ship": {"changed": ["dev"], "ships": true, "unobserved": ["lina", "investing", "legal"]}}
```

| Agent | Cases | Baseline passes | Tailored passes | Verdict |
| --- | --- | --- | --- | --- |
| dev | 2 | 1 | 2 | improved |
| life | 1 | 1 | 1 | unchanged |
| creative | 1 | 1 | 1 | unchanged |
| knowledge | 1 | 1 | 1 | unchanged |
| system | 1 | 1 | 1 | unchanged |

Seen: the runner reads a private case file, plays each case per variant, applies
the five deterministic checks, and applies the ship rule. The report carries case
ids, check outcomes, counts and verdicts, and no case or reply text. Injected
user turns are dropped before a case is played, and a case left without an owner
turn is refused rather than scored.

Inferred: the untailored side is the shipped layers with the empty work-method
record, so the default run isolates the record's contribution. The M4-3 layer
edits (#619) are measured by passing `--baseline` a snapshot of the pre-M4-3
triple, for example extracted from the commit that shipped #584.

Unknown: the real before and after numbers. The M4-1 replay set is private and
outside this repository and was not reachable on the machine that built the
runner, so no case was played from it. Nothing here claims that the tailoring
improves any agent; that claim needs one observed run per case and variant on the
owner's host.

## Limits

- The checks are text measures, not a judge. They cannot score whether a reply was
  right, only whether it broke a stated expectation; a reply can pass every check
  and still be wrong.
- `no_question` and `done_evidence` use marker lists. A question phrased outside
  the listed Korean endings, or a done claim phrased without a listed verb, is not
  detected; the lists are in `internal/core/methodreplay/score.go`.
- `language` ignores lines with fewer than ten letters, so a short line in another
  language passes.
- A single run per case is noisy. A ship decision uses `--repeat 3`.
- The measurement covers what one model produced at one time; it is not evidence
  about another model or a later revision.
