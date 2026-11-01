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
