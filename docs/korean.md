# Natural Korean

The owner is Korean, so natural Korean is a core feature of every LINA agent (lina#376).

## The labeled set and `lina eval korean`

`lina eval korean` scores every Korean check registered with it on one labeled set kept in the store (lina#377).
The set is the yardstick: a baseline is taken before a check or a prompt change lands, and each later part of
lina#376 is measured against it.

### What the set holds

`memory.korean_eval_texts` (migration `0017_eval_korean.sql`) holds two kinds of text:

| Kind | What it is | Label |
| --- | --- | --- |
| `flagged` | a sentence the owner flagged | `phrase`: the words flagged, found verbatim in the sentence; one row per phrase |
| `clean` | a text a person marked "nothing to fix" | `marked_by`: `owner`, or `reviewer` with `marker_family` (the reviewer agent's model family) and `writer_family` (the writing agent's) |

A reviewer's mark counts only from a model family other than the writer's; the table refuses the rest. A reply the
owner simply let pass is not a clean label: letting it pass judges the content, not the language. Every row also
carries `source` (a short label of where it came from), `agent_id` (the writing agent, empty when unknown) and
`created_at`. The flagged -> fixed pairs of the shared word list (lina#378) join the set through
`word_list_pairs()` in `hermes_cli/korean/eval.py`; until that list exists it gives none, so the report shows
`0 word-list pairs`.

### Importing

```sh
lina eval korean import --flagged flagged.tsv
lina eval korean import --clean clean.tsv --source review-1
```

Both files are UTF-8, one text per line, fields separated by a tab; trailing optional fields may be left out.

| File | Columns |
| --- | --- |
| `--flagged` | `sentence`, `phrase`, `agent` (optional), `source` (optional) |
| `--clean` | `text`, `marked_by` (`owner` or `reviewer`), `marker_family`, `writer_family`, `agent` (optional), `source` (optional) |

`marker_family` and `writer_family` are required for `reviewer` and must differ; an `owner` line leaves
`marker_family` empty. `source` falls back to `--source` (default `import`). No line is dropped: each one is
imported, counted as already in the set (a second import of the same file adds nothing), or refused with its line
number and a reason (empty line, not UTF-8, too many columns, no phrase, the phrase not in the sentence, a bad or
same-family mark, the same text already labeled the other kind). The command exits 1 when a line was refused.

### Scoring

A check is a callable from one text to the character spans it flags (`[start, end)`), registered with
`hermes_cli.korean.eval.register(name, check)`. No check is registered yet (they are lina#381); the command then
counts the set only. Per check the report gives, each as `k/n`, a rate and a 95% Wilson interval:

- **flagged sentences caught**: flagged sentences with at least one span;
- **flagged phrases caught**: flagged phrases that a span overlaps;
- **false alarms on clean texts**: clean texts with at least one span.

With fewer than 50 clean texts the report says `smoke only`, and no rate may be used as a gate: `--gate` then
refuses with exit 2. With 50 or more, `--gate` exits 1 when a check's false-alarm rate may exceed
`--max-false-alarm` (default 0.1), judged on the upper end of its interval.

`--save-baseline NAME` stores the run's numbers in `memory.korean_eval_baselines` (counts only; a name is never
replaced). A later run compares with the newest baseline, or with `--baseline NAME`, and prints each rate's
change in points beside the baseline's `k/n`.

### Privacy

The set is the owner's own words and the agents' replies to the owner. It lives in the store only: never in the repository,
an issue, a log or a report. `lina eval korean` prints counts and intervals only, and import errors name a line
number and a reason, never the line. Tests use a small invented set (`tests/hermes_cli/test_korean_eval.py`). The
table follows the store's row-level rules ([store.md](store.md#roles-and-row-level-security)): an agent reads only
the texts it wrote, no agent reads an unowned one, and the command runs as the store owner.
