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
`created_at`. The shared word list's stored correction pairs join through `word_list_pairs()` in
`hermes_cli/korean/eval.py`. Each pair attached to an active entry contributes that entry's avoid word as
a flagged sentence and phrase. These word-only samples measure vocabulary coverage; import a labeled
sentence to measure the correction in context.

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
`hermes_cli.korean.eval.register(name, check)`. The default registry contains `word-list`, `particles`,
and `calques` (lina#381). A caller that explicitly configures its own registry keeps that selection.
Per check the report gives, each as `k/n`, a rate and a 95% Wilson interval:

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

## The owner's own word for a concept

Before an agent names a concept in Korean, it looks up what the owner called it in his own messages (lina#379). If he
used a term, the agent uses it; if he never named the concept, the agent describes it in plain words and does not coin
a name. Putting the found terms in the prompt is lina#380; checking a reply for coined compounds is lina#381.

### The tool and the functions

The lina-memory tool `owner_terms` takes one `concept`, a plain Korean phrase (`취향 목록`) or an English term
(`taste slot`), and answers:

```json
{"concept": "taste slot", "terms": [{"term": "취향 목록", "count": 2, "uids": ["agent:alpha:..."]}], "use": "취향 목록"}
```

- `terms`: his terms, most used first (at most 10); `count` is how many of his messages used the term, `uids` their
  `memory.messages` uids, newest first (at most 20). `memory_open msg:<uid>` opens one.
- `use`: the term to write (`preferred_term()`), or null.
- With no term, `terms` is empty, `use` is null and `hint` is `describe it in plain words; do not coin a name`.

The tool calls `hermes_cli.korean.lookup`: `owner_terms(store, concept)` (a list of `OwnerTerm`), `lookup(store,
concept)` (the tool's answer), `stems(text)`, `compounds(text)`, `reference_words()` and `is_coined(compound,
owner_terms, reference_words)`.

### Whose rows

Only rows whose speaker is `owner` (`memory.messages.speaker`, lina#324) are read: an agent's reply, a tool result or
someone outside never makes a word the owner's, even in the same conversation. As in `memory_search`, row-level
security decides which rows the agent may read, forgotten rows and blocks the host injected as user turns are left out,
an edited message counts once as its newest version, and the owner's external messages are read only while
`memory.external_messages.enabled` is on. A lookup reads his newest 5,000 messages that hold the concept's nouns
(every noun of a Korean concept, every word of an English one). The answer never carries the text of a message: only
terms, counts and uids.

### Matching

Matching goes through Kiwi's morphological analysis, so endings and particles do not hide a match:

- a predicate is matched by its dictionary form: 봤어, 봤다 and 보고 are all 보다;
- a run of nouns, written together or with spaces between, is matched by the nouns joined: 취향 목록을 and 취향목록에
  are both the concept 취향 목록, and stay two terms in his own spelling (`취향 목록`, `취향목록`). The run may sit inside
  a longer one (취향 목록 in 취향 목록 정리해줘);
- the units of a concept must follow one another with only particles, endings and suffixes between;
- a concept in dictionary form (`보다`, `만들다`) is read as a predicate when Kiwi offers that reading, though its first
  reading may be another word (보다 the adverb).

An English term (no Hangul) is found as whole words in his messages, case and spacing aside. The answer is then the
Korean noun runs of those messages, by how many of them use each and then by how near they stand to the English words:
an owner message "taste slot은 취향 목록이라고 부르자" makes `taste slot` answer 취향 목록. An English design doc is not
his message, so it never adds a term.

### Coined compounds

`compounds(text)` gives the compound nouns of a text: two or more nouns written as one word (취향칸), or a single noun
Kiwi does not know. `is_coined(compound, owner_terms, reference_words)` is true when the compound (spaces ignored) is
neither one of his terms nor in the reference list. The reference list, `reference_words()`, is the nouns of Kiwi's
built-in dictionary: its model's morpheme list, built from the Sejong corpus. It knows 일정표 and 오늘밤 but not every
real compound (택배함 is not in it); such a word counts as coined until the owner uses it.

### Kiwi

Kiwi is `kiwipiepy` (with its model `kiwipiepy_model`, about 90 MB), installed by the opt-in extra `korean`. The
first lookup on a machine without it installs the extra through PM (`pm.ensure_import`); when that cannot run the tool
answers with an error naming `hermes pm install --extra korean`. Its license is Apache-2.0 (`kiwipiepy` and
`kiwipiepy_model` on PyPI, the Kiwi repository's `LICENSE`), which may be combined with LINA's AGPL-3.0. The tests
skip with a reason only where `kiwipiepy` is not installed; the test environment gets it with
`source ./activate --test-extras all,korean`.
## Deterministic reply checks

`hermes_cli.korean.checks.check_reply(text, entries=None, *, names=None)` returns a `CheckResult` with
`text`, applied `edits`, `dropped`, `reason`, and advisory `calques` spans. With no supplied entries it reads
the active word list through the agent read API. It makes no model call. Pass the complete inventory of
names from the writing context; `names=()` explicitly declares that there are none. Without an inventory,
a proposed edit is dropped as `names_unavailable`: this checker does not pretend to recognize every
proper name from its spelling.

The scanner matches whole listed words or phrases, including an immediately attached particle, longest
first. It recognizes both avoid and preferred words, so a preferred noun with a stale particle can be
repaired too. It does not guess nouns from arbitrary suffixes: for example, the last character of
`사과` alone is not evidence of a particle. Replacements are planned against original character offsets,
never cascaded through the word list, and touch only their matched spans. An ambiguous avoid word with
multiple preferred forms returns the original with `check_error`.

Particle selection uses the final composed Hangul syllable's jongseong index, `(ord(last) - 0xAC00) % 28`,
for `을/를`, `이/가`, `은/는`, `과/와`, and `으로/로`. Index 8 (final ㄹ) takes `로`, as does a vowel.
A final character outside composed Hangul keeps the supplied particle; no pronunciation library is used.

All edits are rejected together if they change a numeric expression, a negation, or a supplied name.
The numeric guard includes digit expressions and common Korean counters; the negation guard covers
`안`, `못`, inflected negative predicates, and prohibitions such as `마세요`. Numeric recognition also
includes native number words without counters. These conservative lexical guards can reject homonyms.
More than 30% changed characters also rejects the whole fix (`change_ratio`); exactly 30% is allowed.
Each non-equal block inside a matched span counts the larger of its original and replacement lengths,
including inserted characters, against the original text length. Unchanged surrounding text is preserved
byte for byte. Any ordinary exception at this boundary returns the original with `check_error`; exception
messages and input text are never logged. The caller sends the returned text regardless of `dropped`.

### Calques and evaluation

`hermes_cli/data/korean_calques.json` contains a small catalog written for LINA, with an original `before` and `after` example
for every entry. Catalog tests require each example to be flagged and its rewrite to be clean. Catalog
hits are advice only: they never rewrite the reply. The density gate is **at least 2 hits per 100 Unicode
characters**, counting occurrences across the whole original text, including spaces. Below that density,
the catalog returns no spans. Literal contexts can still match; these hints are not claims of an error.

Evaluation loads defaults through `register` while loading the word-list samples. It refreshes the word
snapshot on each evaluation and uses the existing owner connection to call `words.entries` and
`words.pairs`. Agents do not gain access to private pairs. Evaluation detects proposed word and particle
edits even when the reply safety boundary would decline to apply them, and prints counts only.

### Report reader and integration boundary

`read_report(Document(text, kind, writer_family), reader)` accepts the `Reader` protocol (`family` and
`notes(text)`). It runs only when `kind == "report"`, the text has at least **1,000 characters**, and both
families are nonempty and different after trimming and case folding. `StubReader` supplies explicit stub
advice without calling a model. The result always retains the original text; its notes go to the writing
agent, never to a rewriting pipeline.

The existing final-answer boundary is `agent/turn_final_response.py`'s call to
`agent.turn_finalizer.apply_llm_output_transform`, before publication and transcript persistence.
This change exposes the documented callable only and does not wire it into that boundary or any send path.

Run `python scripts/korean_checks_probe.py --synthetic` for the 20-reply synthetic demonstration,
`--store` against a newly installed disposable store for word-list and CLI evaluation, and
`--benchmark` for median added time on a 1,000-character reply. Timing is reported, never a CI limit.
