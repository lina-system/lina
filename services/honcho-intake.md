# Honcho intake ledger

`services/honcho/` is a `git subtree` (squashed) of [plastic-labs/honcho](https://github.com/plastic-labs/honcho).
The `honcho-upstream` branch mirrors Honcho `main` with its full history; the procedure is in
[docs/honcho.md](../docs/honcho.md).

## Intakes

One row per subtree add or pull, newest last. `scripts/ci/check_honcho_subtree.py` reads the last row: its
upstream commit must be the `git-subtree-split` of the newest squash under `services/honcho` and must be
named in the modification notice at the top of `services/honcho/README.md`.

| Date | Upstream ref | Upstream commit | Method |
| --- | --- | --- | --- |
| 2026-11-02 | v3.5.0 | 0cd37ddbd88f51cdac7c660d7372f4b2fd40a4b3 | `git subtree add --squash` |

## LINA changes

Changes LINA carries in `services/honcho/` on top of the upstream commit above. The LICENSE file and the
copyright and license headers of upstream files are never changed.

| Path | Change | Since |
| --- | --- | --- |
| `README.md` | Modification notice at the top (AGPL-3.0 section 5(a)) | 2026-11-02 |
| `scripts/release_manifest.py` | Read `<ref>:./<path>` (relative to the project directory, `ls-tree --full-tree`) instead of `<ref>:<path>` (relative to the repository root), so `test_heads_match_alembic` passes when Honcho is a subdirectory | 2026-11-02 |
| `src/message_store.py`, `src/models.py`, `src/crud/session.py`, `migrations/versions/l13_message_split.py`, `tests/` | Split immutable text into shared `memory.messages` and metadata into `ltm.message_meta`; retain public IDs, remap integer references, write atomically, detach deleted message source edges, and cover migration, API, rollback and cascade behavior (lina#13) | 2026-11-03 |
| `src/crud/message.py`, `src/crud/session.py`, `src/deriver/deriver.py`, `src/deriver/queue_manager.py`, `src/utils/summarizer.py`, `migrations/versions/l13_message_split.py`, `tests/` | Preserve chronology with `seq_in_session` after shared-ID remapping; remap short/long summary identities atomically, resolve boundary IDs to session positions, and run the split after `l19_vec_embeddings` (lina#13) | 2026-11-03 |
| `src/models.py`, `src/vec_embeddings.py` | Vectors live in LINA's `vec.embeddings` (lina#19): new model `VecEmbedding`; `Document.embedding` and `MessageEmbedding.embedding` are properties over a `vec_row` relationship (kind `document` keyed by the new `documents.vec_id`, kind `message` keyed by `message_embeddings.id`, model = `EMBEDDING_MODEL_CONFIG__MODEL`, agent_id = workspace); delete triggers drop a row's vectors with it | 2026-11-03 |
| `src/crud/document.py`, `src/crud/message.py`, `src/utils/search.py`, `src/deriver/scope_backfill.py`, `src/reconciler/embed_now.py`, `src/reconciler/sync_vectors.py` | Vector searches join `vec.embeddings`; statement-level vector writes go through `vec_embeddings.put_vectors` | 2026-11-03 |
| `src/startup/embedding_validator.py`, `scripts/configure_embeddings.py` | Check and alter the width of `vec.embeddings.embedding` instead of the two dropped columns | 2026-11-03 |
| `migrations/versions/l19_vec_embeddings.py`, `src/config.py` | Revision `l19_vec_embeddings`: creates `vec.embeddings` when the store has not, adds `documents.vec_id`, copies vectors as wide as the table only when the new setting `EMBEDDING_LEGACY_MODEL` names the configured model, counts the rest in its log (message chunks back to pending), adds the delete triggers, drops `documents.embedding` and `message_embeddings.embedding` | 2026-11-03 |
| `tests/alembic/revisions/test_l19_vec_embeddings.py`, `tests/alembic/test_l19_legacy_model.py`, `tests/alembic/conftest.py`, `tests/startup/test_embedding_validator.py`, `tests/scripts/test_configure_embeddings.py`, `tests/test_models_vector_dim.py` | Tests for the revision; the alembic reset also drops schema `vec`; the dimension tests read `vec.embeddings` | 2026-11-03 |
