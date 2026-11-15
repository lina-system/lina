# Natural Korean

The owner is Korean, so natural Korean is a core feature of every LINA agent (lina#376). This page describes the parts that
have landed.

## The owner's word list

One list, shared by every agent: a word to avoid, the word to use instead, a reason code, and the
`memory.messages` uid of the correction it came from (lina#378). It grows from the owner's corrections
("'칸' 말고 '목록'"), from one sentence of his, and from a seed file.

```sh
lina words add --avoid 칸 --use 목록 --reason calque [--source <uid>] [--flagged <sentence> --fixed <sentence>]
lina words retire --avoid 칸 [--use 목록] [--source <uid>]
lina words list [--all] [--json] [--with-pairs]
lina words import seed.tsv
```

| Reason | Meaning |
| --- | --- |
| `calque` | an English word carried over into Korean (English "slot" written as "칸") |
| `coined` | a new name nobody uses |
| `translationese` | a phrase built like the English sentence |
| `owner_term` | the owner's own word for a concept |
| `other` | none of these |

- Words are stored in Unicode NFC with surrounding and repeated spaces removed, so the same word typed
  two ways is one word.
- The same correction twice (the same avoid and use word) leaves one active entry; the entry keeps the first
  source uid, and a new pair joins it.
- `retire` marks the active entries for a word retired (with the uid of the sentence that retired them, when
  given). The next read leaves them out; the row stays, so `list --all` still shows it.
- `import` reads `avoid<TAB>use<TAB>reason` lines. Every line is added, found already in the list, or reported on
  stderr with its line number and why (wrong column count, empty word, unknown reason, the same word twice). The
  exit code is 0 when every line is in the list, 1 when a line was reported, 2 when the store cannot be reached.
- The context builder reads the list through `hermes_cli.korean.words.word_list_entries(terms=None)`: the active
  entries, or with `terms` only those whose avoid or use word appears in one of them (a word, a phrase or a whole
  message). Where the entries go in the prompt is lina#380.

### Scope and who writes it

The list lives in `memory.word_list` (migration `0016_word_list.sql`). Its rows have no `agent_id`: no agent owns
them. As for the owner-level external messages (`0005`, `0009`, [store.md](store.md)), a SELECT-only policy lets
every agent read the list through `lina_app`, whatever agent the session acts for, and nothing lets an agent write
it: `lina_app` has no INSERT, UPDATE or DELETE grant and no write policy. Two paths write it:

- the owner path, `lina words` on the store's host (role `lina`);
- the refiner (lina#198), through the role `lina_refiner` (NOLOGIN; a refiner connection takes it with
  `SET ROLE lina_refiner`), which may read, add and retire entries and add pairs, but not delete them.

An agent that recognises a correction cannot write it itself; it is written by the refiner or the owner.

### Pairs are private

Each correction can keep the flagged sentence and the fixed sentence as a pair in `memory.word_pairs`, for the
measurement run (lina#377). Pairs are the owner's own text:

- no agent can read them: `lina_app` has no grant on `memory.word_pairs`;
- `lina words list` and `list --json` (the export) never include them; only `--with-pairs`, run locally by the owner,
  prints them;
- the code never logs them: log lines name the entry id and reason only, and a `WordPair`'s `repr` leaves the
  sentences out;
- never copy them into an issue, a commit, a test fixture or a report. Test fixtures are synthetic.

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
