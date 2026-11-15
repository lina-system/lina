# Natural Korean

The owner is Korean, so natural Korean is part of every agent (lina#376). This page describes the parts that
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
