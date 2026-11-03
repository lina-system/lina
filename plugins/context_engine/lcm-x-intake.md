# LCM-X intake ledger

`plugins/context_engine/lcm-x/` is a `git subtree` (squashed) of [electricsheephq/lcm-x](https://github.com/electricsheephq/lcm-x)
(MIT). The `lcm-x-upstream` branch mirrors LCM-X `main` with its full history; the procedure is in
[docs/lcm-x.md](../../docs/lcm-x.md).

## Intakes

One row per subtree add or pull, newest last. `scripts/ci/check_lcm_x_subtree.py` reads the last row: its
upstream commit must be the `git-subtree-split` of the newest squash under `plugins/context_engine/lcm-x`.

| Date | Upstream ref | Upstream commit | Method |
| --- | --- | --- | --- |
| 2026-11-02 | v0.29.1-rc2 | 0eb6e8736ac0361341b3f29ebfb8e30e5b4dabc5 | `git subtree add --squash` |

## LINA changes

Changes LINA carries in `plugins/context_engine/lcm-x/` on top of the upstream commit above, each an
ordinary commit on `dev`. They are the patches of the `hermes` branch of the
[thisisjun786/lcm-x](https://github.com/thisisjun786/lcm-x) fork at `daa9e9950d247dc86f47e0a6e1e77bc49420f05f`,
the build every agent ran before LCM-X was bundled. The first LINA commit carries the fork's 13 patches
before its merge of v0.29.1-rc2 (`28ff276d`), as that merge resolved them; the next five are the fork's
commits after it. The LICENSE file and the copyright and license headers of upstream files are never
changed. When upstream fixes the same problem, prefer upstream's fix and remove ours, and its row, in the
intake pull request. The last rows are LINA's own changes, with no fork commit.

| Path | Change | Fork commit |
| --- | --- | --- |
| `compaction.py`, `store_complete.py` | Name which empty-leaf-input branch stopped compaction (superseded by upstream #1599 in the rc2 merge) | `b9dc3b47` |
| `store_complete.py` | Stop the endless no-op compaction when an older row's owner sits behind newer replay copies | `6d94edf6` |
| `store_complete.py` | Keep composite carriers whole when rehydrating an out-of-order claim | `eb064a24` |
| `tests/fixtures/compress_trace.json` | Regenerate the compress golden trace for `_store_complete_empty_reason` (superseded in the rc2 merge) | `68874e4f` |
| `engine.py` | Recognise a gateway restart replay by `message_uid` so the history is not stored again | `efa0f0a5` |
| `engine.py` | Limit the `message_uid` replay check to rows the host sent without a timestamp | `c9ebff36` |
| `engine.py` | Do not store LCM's own summary row again on every CLI resume | `c43f275a` |
| `engine.py`, `fork_summary_quarantine.py`, `scripts/fork_quarantine_summary_rows.py` | Never store LCM's own emitted summary head on a cold bind; a lossless repair for stores it wedged | `3de31a1a` |
| `engine.py`, `store_complete.py` | Pin uid-bound view occurrences to their own row; opt-in fixes for restamped tails and composite holes | `ae789441` |
| `engine.py`, `reconcile.py`, `fork_copy_quarantine.py`, `scripts/fork_quarantine_copy_rows.py` | Stop the restamped-tail re-store by default; keep a late re-stored copy from orphaning its neighbours | `72518918` |
| `plugin.yaml`, `tokens.py` | Declare `tiktoken` for the host's dependency manager; cache the BPE file under `HERMES_HOME` | `2133610f` |
| `engine.py`, `compaction.py`, `config.py` | Pair host rows with stored rows by message UID first | `79922e2d` |
| `host_uid.py`, `store.py`, `scripts/fork_backfill_uid_bindings.py` | Bind late-stamped host UIDs to stored rows | `36687967` |
| `tests/` | Classify fork env reads and size guards for v0.29.1-rc2 | `d45eae27` |
| `compaction.py`, `engine.py`, `config.py` | Keep preserved-objective rows from stalling the frontier | `d62ffd3b` |
| `.gitignore`, `.hypothesis/` | Stop tracking the hypothesis example cache | `047eb0a2` |
| `compaction.py` | Skip already-summarized host groups before choosing the leaf chunk | `a98e2315` |
| `compaction.py`, `config.py`, `engine.py` | Make the threshold sweep pass budget configurable (`lcm.threshold_full_sweep_max_passes`) | `daa9e995` |
| `tests/compat/conftest.py`, `tests/differential/trees.py` and seven `tests/test_*.py` that archive an old release | `git -C <plugin dir> --work-tree=. archive <commit>`: inside LINA the plugin dir is a subdirectory, and a plain `git archive` there archives only that (absent) subdirectory of the old commit | LINA |
| `CHANGELOG.md`, `engine.py`, `reconcile.py`, `store_complete.py`, `fork_copy_quarantine.py`, `scripts/fork_quarantine_summary_rows.py` and six fork tests | Comments, docstrings and CHANGELOG lines of the carried fork patches describe the incidents in neutral terms (no session ids, profile names, host paths, production row counts, clock times or dates); code and assertions unchanged | LINA |
| `sqlite_session.py` | `open_connection` returns the host's store connection when LINA routes the database to its Postgres (`plugins/context_engine/lcm_x_store.py`, lina#16); `is_store_connection` names one | LINA |
| `db_bootstrap.py`, `query_view_store.py` | The schema creators, verifiers and FTS repairers return at once on a store connection: the store's migrations own that schema (lina#16) | LINA |
| `store.py`, `dag.py` | On a store connection: no DDL at open, the host `message_uid` passed with each stored message (its text is the row Hermes wrote), ranked search rows read plainly instead of through the JSON1 packing (lina#16) | LINA |
| `tests/test_issue_852_command_profile.py` | `test_unresolved_other_bound_session_refuses_write_allows_read` compares two `/lcm status` reads with the `embedding_incremental_last_pass` timestamp normalised: a background incremental pass can stamp the next second between them (not fixed upstream as of `lcm-x-upstream`; upstream main normalises only `database_size` there). Candidate upstream PR | LINA |
