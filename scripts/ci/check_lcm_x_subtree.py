#!/usr/bin/env python3
"""Check the LCM-X subtree in ``plugins/context_engine/lcm-x`` against the upstream commit it came from.

``plugins/context_engine/lcm-x`` is a squashed ``git subtree`` of electricsheephq/lcm-x (docs/lcm-x.md),
checked like the Honcho subtree (``scripts/ci/subtree_check.py``): the ledger's last upstream commit is
the newest squash's split, upstream LICENSE files are unchanged and upstream header lines are kept. LCM-X
is MIT, so there is no modification notice.

    python scripts/ci/check_lcm_x_subtree.py
    python scripts/ci/check_lcm_x_subtree.py --mirror origin/lcm-x-upstream

Exit codes: 0 = clean, 1 = at least one finding, 2 = git or usage error.
"""

from __future__ import annotations

import sys
from pathlib import Path

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from scripts.ci.subtree_check import Subtree, main as _main

LCM_X = Subtree(name="check_lcm_x_subtree", prefix="plugins/context_engine/lcm-x",
                ledger="plugins/context_engine/lcm-x-intake.md", mirror="lcm-x-upstream", modification_notice=False)


def main(argv: list[str] | None = None) -> int:
    return _main(LCM_X, __doc__.split("\n\n")[0], argv)


if __name__ == "__main__":
    raise SystemExit(main())
